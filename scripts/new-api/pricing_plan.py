"""Read-only planning for Forest upstream catalogue. No network or credentials.

A plan is NOT proof of route health or billing. Apply only after target preview,
request testing, and refreshed optimistic-lock versions, never by raw SQL.
"""
from __future__ import annotations
import argparse, copy, hashlib, json, math, re
from decimal import Decimal, InvalidOperation, localcontext
from pathlib import Path

MARKUP=Decimal('2.5')
SOURCE_CNY_PER_UNIT=Decimal('1')
TARGET_CNY_PER_UNIT=Decimal('7.3')
TARGET_GROUP_RATIO=Decimal('1')
LUNA_EXCLUSION='Owner-confirmed policy 2026-11-11: Luna models and dedicated Luna routes are not offered'
ALLOWED_GROUPS=frozenset({'gpt-优秀','gpt-稳定','gpt-还行','max-gh','max-codez','max-sh','cckiro-不降智','cckiro','grok','gpt-image','doubao','国模'})
EXCLUDED_GROUPS={'codex-test':'test group','default':'upstream labels this group 勿选','ds4':'upstream labels this group 勿选','cc特别分组':'special-purpose group, not confirmed for resale','luna专用':LUNA_EXCLUSION}
# Only a reviewed label-based exclusion may be reconsidered. This is not a
# generic bypass for test, private, dedicated, or previously unknown groups.
REVIEWABLE_GROUPS=frozenset({'ds4'})
RATIO_FIELDS={'completion_ratio':'CompletionRatio','cache_ratio':'CacheRatio','create_cache_ratio':'CreateCacheRatio','image_ratio':'ImageRatio','audio_ratio':'AudioRatio','audio_completion_ratio':'AudioCompletionRatio'}
ALLOWED_TARGET_FIELDS=frozenset({'ModelRatio','ModelPrice','CompletionRatio','CacheRatio','CreateCacheRatio','ImageRatio','AudioRatio','AudioCompletionRatio','billing_setting.billing_mode','billing_setting.billing_expr'})

def dec(value):
    if isinstance(value,bool): raise ValueError('boolean is not a price')
    try: number=Decimal(str(value))
    except InvalidOperation as exc: raise ValueError('invalid price/ratio') from exc
    if not number.is_finite() or number<0: raise ValueError('price/ratio must be finite and non-negative')
    return number

def coefficient(ratio, source_price=SOURCE_CNY_PER_UNIT, target_price=TARGET_CNY_PER_UNIT, target_group=TARGET_GROUP_RATIO):
    source_price,target_price,target_group=map(dec,(source_price,target_price,target_group))
    if not target_price or not target_group: raise ValueError('target unit and group multiplier must be positive')
    with localcontext() as ctx:
        ctx.prec=40
        return dec(ratio)*source_price*MARKUP/target_price/target_group

def decimal_string(value):
    return format(value,'f').rstrip('0').rstrip('.') if '.' in format(value,'f') else format(value,'f')

def scale_expression(expr, factor):
    # New API's fixed-price AST validator rejects an arbitrary outer scalar.
    # Rewrite only a fully recognised single fixed tier (optional image count),
    # never search/replace arbitrary remote expressions or strings in them.
    fixed = re.fullmatch(r'\s*tier\(\s*("(?:\\.|[^"\\])*")\s*,\s*fixed\(\s*(\d+(?:\.\d*)?|\.\d+)([eE][+-]?\d+)?\s*\)\s*\)\s*(\*\s*image_count)?\s*', expr)
    if fixed:
        with localcontext() as ctx:
            ctx.prec = 40
            price = dec(fixed.group(2) + (fixed.group(3) or '')) * factor
        quantity = ' * image_count' if fixed.group(4) else ''
        return f'tier({fixed.group(1)}, fixed({decimal_string(price)})){quantity}'
    if re.search(r'\bfixed\s*\(', expr):
        raise ValueError('complex fixed-price expression requires separately validated leaf scaling')
    return f'({expr}) * {decimal_string(factor)}'

def draft_prices(model, ratio):
    """Create a complete model draft; no prior model options are carried over.

    Missing legacy optional ratios are intentionally absent, not replaced by
    guessed zero/defaults. Callers MUST compare the target preview's effective
    defaults to the upstream's actual logged usage before route activation.
    """
    factor=coefficient(ratio)
    mode=model.get('billing_mode')
    if mode not in (None,'','ratio','tiered_expr'):
        raise ValueError('unsupported upstream billing mode')
    warnings=[]
    if mode=='tiered_expr':
        expr=model.get('billing_expr')
        if not isinstance(expr,str) or not expr.strip(): raise ValueError('billing expression missing')
        if expr.startswith('v1:'): expr=expr[3:]
        if len(expr)>65536: raise ValueError('billing expression too long')
        # Token rules retain every branch/cache condition. Fixed-price rules
        # need narrowly validated literal scaling; see scale_expression.
        draft={'billing_setting.billing_mode':'tiered_expr','billing_setting.billing_expr':scale_expression(expr,factor)}
        kind='expression'
    elif model.get('quota_type')==1:
        if 'model_price' not in model: raise ValueError('per-call price missing')
        draft={'billing_setting.billing_mode':'ratio','ModelPrice':float(dec(model['model_price'])*factor)}
        kind='per_call'
    elif model.get('quota_type')==0:
        if 'model_ratio' not in model or 'completion_ratio' not in model: raise ValueError('legacy input/output ratio missing')
        draft={'billing_setting.billing_mode':'ratio','ModelRatio':float(dec(model['model_ratio'])*factor)}
        for src,target in RATIO_FIELDS.items():
            if src in model: draft[target]=float(dec(model[src]))
        for src in ('cache_ratio','create_cache_ratio','image_ratio','audio_ratio','audio_completion_ratio'):
            if src not in model: warnings.append(f'{src} not exposed upstream; target preview/default and observed usage require verification')
        kind='ratio'
    else: raise ValueError('unknown quota type')
    assert set(draft)<=ALLOWED_TARGET_FIELDS
    if any(isinstance(v,float) and not math.isfinite(v) for v in draft.values()): raise ValueError('target price overflow')
    return draft,kind,warnings

def validate_group_reviews(reviews):
    if reviews is None: return {}
    if not isinstance(reviews,dict): raise ValueError('reviewed groups must map group names to review reasons')
    clean={}
    for group,reason in reviews.items():
        if group not in REVIEWABLE_GROUPS:
            raise ValueError(f'{group}: group is not eligible for a label-review override')
        if not isinstance(reason,str) or not reason.strip() or len(reason)>1000:
            raise ValueError(f'{group}: a non-empty review reason of at most 1000 characters is required')
        clean[group]=reason.strip()
    return clean

def parse_group_reviews(values):
    reviews={}
    for value in values:
        group,separator,reason=value.partition('=')
        if not separator or not group or group in reviews:
            raise ValueError('each --reviewed-group must be a unique GROUP=REVIEW_REASON')
        reviews[group]=reason
    return validate_group_reviews(reviews)

def group_exclusion(model_name, group, include_luna_for_all=False, reviewed_groups=None):
    # Retain the old Python signature for callers passing False, but never let
    # a saved flag silently reinstate the superseded Luna resale policy.
    if include_luna_for_all: raise ValueError('include_luna_for_all is no longer supported: '+LUNA_EXCLUSION)
    if 'luna' in model_name.casefold() or 'luna' in group.casefold(): return LUNA_EXCLUSION
    reviews=validate_group_reviews(reviewed_groups)
    if group in reviews: return None
    if group in EXCLUDED_GROUPS: return EXCLUDED_GROUPS[group]
    if re.search(r'test|测试|试用',group,re.I): return 'test/trial group'
    if group.startswith('g-'): return 'user-specific group'
    if group not in ALLOWED_GROUPS: return 'unreviewed group'
    return None

def build_plan(audit, include_luna_for_all=False, reviewed_groups=None):
    if include_luna_for_all: raise ValueError('include_luna_for_all is no longer supported: '+LUNA_EXCLUSION)
    reviews=validate_group_reviews(reviewed_groups)
    catalog=audit['pricing']['data']
    if catalog.get('success') is not True: raise ValueError('source pricing request unsuccessful')
    ratios=catalog.get('group_ratio') or {}
    own_result=(audit.get('groups') or {}).get('data',{})
    own_verified=own_result.get('success') is True
    if not own_verified: raise ValueError('authenticated usable-group snapshot required')
    own=own_result.get('data',{}) if own_verified else {}
    all_models=catalog['data']
    if len({m['model_name'] for m in all_models})!=len(all_models): raise ValueError('duplicate model identifiers')
    planned=[];blocked=[]
    for model in sorted(all_models,key=lambda x:x['model_name']):
        name=model['model_name'];candidates=[];excluded=[]
        if 'luna' in name.casefold():
            blocked.append({'model_name':name,'reason':LUNA_EXCLUSION,'excluded_groups':[{'group':group,'reason':LUNA_EXCLUSION} for group in model.get('enable_groups') or []]});continue
        for group in model.get('enable_groups') or []:
            reason=group_exclusion(name,group,include_luna_for_all,reviews)
            if reason:
                excluded.append({'group':group,'reason':reason});continue
            if own_verified and group not in own:
                excluded.append({'group':group,'reason':'not listed in authenticated usable groups'});continue
            # Logged-in usable-group quote is more specific than public ratios.
            value=own.get(group,{}).get('ratio',ratios.get(group))
            try: ratio=dec(value)
            except Exception:
                excluded.append({'group':group,'reason':'missing or invalid ratio'});continue
            candidates.append({'group':group,'ratio':float(ratio)})
        # A reviewed label does not displace an already-reviewed formal route
        # at the same price. Price still wins first; name is the stable tie-break.
        candidates.sort(key=lambda x:(-x['ratio'],x['group'] in reviews,x['group']))
        if not candidates:
            blocked.append({'model_name':name,'reason':'no reviewed formal group','excluded_groups':excluded});continue
        selected=candidates[0]
        try: draft,kind,warnings=draft_prices(model,selected['ratio'])
        except ValueError as exc:
            blocked.append({'model_name':name,'reason':str(exc),'candidates':candidates});continue
        if selected['group'] in reviews:
            warnings.append('label exclusion explicitly reviewed: '+reviews[selected['group']])
            warnings.append('group review does not prove this model is healthy or billed correctly; per-model verification remains required')
        planned.append({'model_name':name,'source_group':selected['group'],'source_group_ratio':selected['ratio'],'candidates':candidates,'excluded_groups':excluded,'target_group':'default','kind':kind,'internal_factor':decimal_string(coefficient(selected['ratio'])),'pricing':draft,'source_model':copy.deepcopy(model),'warnings':warnings,'verification_state':'not_tested'})
    routes={}
    for item in planned: routes.setdefault(item['source_group'],[]).append(item['model_name'])
    return {'schema_version':1,'source_origin':audit['origin'],'source_sha256':hashlib.sha256(json.dumps(catalog,sort_keys=True,ensure_ascii=False,separators=(',',':')).encode()).hexdigest(),'source_model_count':len(all_models),'planned_count':len(planned),'blocked_count':len(blocked),'basis':{'source_cny_per_quota_unit':str(SOURCE_CNY_PER_UNIT),'source_quota_per_unit':500000,'target_cny_per_quota_unit':str(TARGET_CNY_PER_UNIT),'target_group':'default','target_group_ratio':str(TARGET_GROUP_RATIO),'markup':str(MARKUP),'selection':'maximum actual model-compatible reviewed group ratio','include_luna_for_all':False,'luna_policy_pending_owner':False,'luna_policy':'owner_excluded_all_luna','reviewed_group_overrides':reviews},'routes':routes,'models':planned,'blocked':blocked}

def attach_snapshot(plan,snapshot):
    """Use a fresh GET /api/option/model_pricing result, never guessed hashes."""
    entries={row['model_name']:row for row in snapshot['entries']}
    if len(entries)!=len(snapshot['entries']): raise ValueError('duplicate target models')
    empty=snapshot.get('empty_version')
    if not isinstance(empty,str) or len(empty)!=64: raise ValueError('empty_version missing from current snapshot')
    changes=[]
    for item in plan['models']:
        if 'luna' in item['model_name'].casefold() or 'luna' in item.get('source_group','').casefold():
            raise ValueError(f"{item['model_name']}: {LUNA_EXCLUSION}; regenerate the stale plan")
        previous=entries.get(item['model_name'])
        if previous and previous.get('configured',{}).get('billing_setting.plugin_billing_expr'):
            raise ValueError(f"{item['model_name']}: plugin pricing overrides require manual reconciliation")
        version=previous['version'] if previous else empty
        if not isinstance(version,str) or len(version)!=64: raise ValueError('target version missing')
        changes.append({'model_name':item['model_name'],'expected_version':version,'pricing':copy.deepcopy(item['pricing'])})
    return {'changes':changes}

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--audit',type=Path,required=True,help='Sanitized authenticated catalogue/group JSON snapshot')
    parser.add_argument('--output',type=Path,required=True,help='New local plan JSON, not a live API endpoint')
    parser.add_argument('--include-luna-for-all',action='store_true',help='Removed policy override; rejected if supplied')
    parser.add_argument('--reviewed-group',action='append',default=[],metavar='GROUP=REVIEW_REASON',help='Explicitly reconsider an eligible label exclusion using a recorded runtime/billing review; does not mark models healthy')
    args=parser.parse_args()
    if args.include_luna_for_all: parser.error('--include-luna-for-all is no longer supported: '+LUNA_EXCLUSION)
    try: reviews=parse_group_reviews(args.reviewed_group)
    except ValueError as exc: parser.error(str(exc))
    plan=build_plan(json.loads(args.audit.read_text()),args.include_luna_for_all,reviews)
    args.output.write_text(json.dumps(plan,indent=2,ensure_ascii=False)+'\n');args.output.chmod(0o600)
    print(json.dumps({'planned':plan['planned_count'],'blocked':plan['blocked_count'],'routes':{k:len(v) for k,v in plan['routes'].items()},'output':str(args.output)},ensure_ascii=False))
if __name__=='__main__':main()
