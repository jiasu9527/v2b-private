import copy,hashlib,json,subprocess,sys,tempfile,unittest
from decimal import Decimal
from pathlib import Path
from pricing_plan import build_plan,draft_prices,coefficient,attach_snapshot,group_exclusion,parse_group_reviews

def fixture():
 """Synthetic public prices only; no live account or /tmp dependency."""
 groups={'gpt-优秀':.17,'gpt-稳定':.15,'max-gh':1.8,'max-codez':1.3,
         'cckiro-不降智':.6,'cckiro':.2,'luna专用':10,'ds4':1,'codex-test':99}
 def model(name,enabled):
  return {'model_name':name,'enable_groups':enabled,'billing_mode':'tiered_expr',
          'billing_expr':'tier("standard",p*2+cr*.1+c*10)'}
 models=[model('gpt-6.1-sol',['gpt-优秀','gpt-稳定','luna专用','codex-test']),
         model('gpt-6-astra',['gpt-优秀','gpt-稳定']),
         model('claude-fable-5-1',['max-gh','max-codez']),
         model('claude-sonnet-5-thinking',['cckiro-不降智','cckiro']),
         model('gpt-6-luna',['luna专用']),model('deepseek-v4.1-flash',['ds4'])]
 return {'origin':'https://upstream.example.invalid',
         'pricing':{'data':{'success':True,'data':models,'group_ratio':groups}},
         'groups':{'data':{'success':True,'data':{g:{'ratio':r} for g,r in groups.items()}}}}

class PricingPlanTests(unittest.TestCase):
 def setUp(self): self.audit=fixture()
 def test_plan_counts_and_disjointness(self):
  plan=build_plan(self.audit); self.assertEqual(plan['planned_count'],4);self.assertEqual(plan['blocked_count'],2)
  planned={m['model_name'] for m in plan['models']};blocked={m['model_name'] for m in plan['blocked']}
  self.assertFalse(planned&blocked);self.assertEqual(len(planned|blocked),6)
  self.assertTrue(all(m['target_group']=='default' and m['verification_state']=='not_tested' for m in plan['models']))
 def test_highest_only_model_applicable_groups(self):
  p={m['model_name']:m for m in build_plan(self.audit)['models']}
  self.assertEqual(p['gpt-6.1-sol']['source_group'],'gpt-优秀');self.assertEqual(p['claude-fable-5-1']['source_group'],'max-gh');self.assertEqual(p['claude-sonnet-5-thinking']['source_group'],'cckiro-不降智')
  self.assertNotIn('gpt-6-luna',p)
  self.assertTrue(all(m['source_group_ratio']==max(g['ratio'] for g in m['candidates']) for m in p.values()))
 def test_owner_policy_excludes_luna_and_rejects_old_override(self):
  normal=build_plan(self.audit)
  self.assertFalse(normal['basis']['luna_policy_pending_owner'])
  self.assertFalse(normal['basis']['include_luna_for_all'])
  self.assertEqual(normal['basis']['luna_policy'],'owner_excluded_all_luna')
  self.assertNotIn('luna专用',normal['routes'])
  blocked=next(m for m in normal['blocked'] if m['model_name']=='gpt-6-luna')
  self.assertIn('Owner-confirmed policy',blocked['reason'])
  with self.assertRaisesRegex(ValueError,'no longer supported'):
   build_plan(self.audit,include_luna_for_all=True)
  with self.assertRaisesRegex(ValueError,'no longer supported'):
   group_exclusion('gpt-6.1-sol','luna专用',include_luna_for_all=True)
 def test_luna_models_excluded_case_insensitively_even_on_formal_or_reviewed_routes(self):
  for name,groups in [('gpt-5.6-luna',['gpt-优秀']),('GPT-6-LUNA',['ds4']),('vendor/LuNa-latest',[])]:
   with self.subTest(name=name):
    audit=copy.deepcopy(self.audit)
    source=copy.deepcopy(audit['pricing']['data']['data'][0]);source.update(model_name=name,enable_groups=groups)
    audit['pricing']['data']['data'].append(source)
    plan=build_plan(audit,reviewed_groups={'ds4':'runtime review'})
    self.assertNotIn(name,[m['model_name'] for m in plan['models']])
    self.assertIn('Owner-confirmed policy',next(m for m in plan['blocked'] if m['model_name']==name)['reason'])
    self.assertIsNotNone(group_exclusion(name,'gpt-优秀'))
 def test_luna_dedicated_route_never_changes_normal_model_prices(self):
  normal=build_plan(self.audit)
  clean=copy.deepcopy(self.audit)
  clean['pricing']['data']['data']=[m for m in clean['pricing']['data']['data'] if 'luna' not in m['model_name'].lower()]
  for model in clean['pricing']['data']['data']:
   model['enable_groups']=[g for g in model['enable_groups'] if g!='luna专用']
  self.assertEqual({m['model_name']:m['pricing'] for m in normal['models']},{m['model_name']:m['pricing'] for m in build_plan(clean)['models']})
  for group in ['luna专用','LUNA专用','new-luna-route']:
   self.assertIn('Owner-confirmed policy',group_exclusion('ordinary-gpt',group))
 def test_cli_old_luna_override_fails_before_reading_input_or_writing_output(self):
  with tempfile.TemporaryDirectory() as tmp:
   output=Path(tmp)/'must-not-exist.json'
   result=subprocess.run([sys.executable,str(Path(__file__).with_name('pricing_plan.py')),'--audit',str(Path(tmp)/'missing.json'),'--output',str(output),'--include-luna-for-all'],capture_output=True,text=True)
   self.assertEqual(result.returncode,2)
   self.assertIn('no longer supported',result.stderr)
   self.assertFalse(output.exists())
 def test_forbidden_groups_never_candidates(self):
  for g in ['codex-test','foo-test','测试专用','default','ds4','g-abc','cc特别分组','unknown','luna专用']:
   self.assertIsNotNone(group_exclusion('gpt-6.1-sol',g))
  self.assertNotIn('ds4',build_plan(self.audit)['routes'])
 def test_source_not_mutated(self):
  before=json.dumps(self.audit,sort_keys=True);build_plan(self.audit);self.assertEqual(before,json.dumps(self.audit,sort_keys=True))
 def test_explicit_label_review_records_reason_without_claiming_health(self):
  reason='2026-11-11: nonempty responses and charged billing reviewed; not a perpetual health guarantee'
  plan=build_plan(self.audit,reviewed_groups={'ds4':reason})
  self.assertEqual(plan['planned_count'],5);self.assertEqual(plan['blocked_count'],1)
  self.assertEqual(plan['basis']['reviewed_group_overrides'],{'ds4':reason})
  model=next(m for m in plan['models'] if m['source_group']=='ds4')
  self.assertEqual(model['source_group_ratio'],1)
  self.assertEqual(model['verification_state'],'not_tested')
  self.assertTrue(any(reason in w for w in model['warnings']))
  self.assertTrue(any('per-model verification' in w for w in model['warnings']))
  self.assertEqual(build_plan(self.audit)['blocked_count'],2)
 def test_review_cannot_override_test_private_unknown_or_luna_policy(self):
  for group in ['codex-test','other-test','测试组','试用','g-private','default','cc特别分组','luna专用','unknown']:
   with self.subTest(group=group):
    with self.assertRaisesRegex(ValueError,'not eligible'):
     build_plan(self.audit,reviewed_groups={group:'a purported review'})
  plan=build_plan(self.audit,reviewed_groups={'ds4':'verified independently'})
  gpt=next(m for m in plan['models'] if m['model_name']=='gpt-6.1-sol')
  self.assertEqual(gpt['source_group'],'gpt-优秀')
 def test_review_cannot_bypass_authenticated_permission(self):
  self.audit['groups']['data']['data'].pop('ds4')
  plan=build_plan(self.audit,reviewed_groups={'ds4':'old runtime evidence'})
  self.assertEqual(plan['blocked_count'],2)
  self.assertIn('authenticated',next(m for m in plan['blocked'] if m['model_name']=='deepseek-v4.1-flash')['excluded_groups'][0]['reason'])
 def test_review_cannot_make_a_group_applicable_to_other_models(self):
  plan=build_plan(self.audit,reviewed_groups={'ds4':'verified'})
  for model in plan['models']:
   if model['model_name']!='deepseek-v4.1-flash':
    self.assertNotIn('ds4',[g['group'] for g in model['candidates']])
 def test_group_review_parser_requires_unique_nonempty_reason(self):
  self.assertEqual(parse_group_reviews(['ds4=  actual price=x verified  ']),{'ds4':'actual price=x verified'})
  for values in [['ds4'],['ds4='],['ds4=  '],['=reason'],['ds4=one','ds4=two'],['ds4='+'x'*1001]]:
   with self.subTest(values=values):
    with self.assertRaises(ValueError):parse_group_reviews(values)
  for bad in [[],{'ds4':False},{'ds4':None}]:
   with self.assertRaises(ValueError):build_plan(self.audit,reviewed_groups=bad)
 def test_review_does_not_change_bad_price_handling(self):
  self.audit['pricing']['data']['data'][-1]['billing_expr']=''
  plan=build_plan(self.audit,reviewed_groups={'ds4':'runtime reviewed'})
  self.assertEqual(plan['blocked_count'],2)
  self.assertIn('expression missing',next(m for m in plan['blocked'] if m['model_name']=='deepseek-v4.1-flash')['reason'])
 def test_equal_price_keeps_formal_route_before_label_override(self):
  self.audit['pricing']['data']['data'][0]['enable_groups'].append('ds4')
  self.audit['groups']['data']['data']['ds4']['ratio']=.17
  plan=build_plan(self.audit,reviewed_groups={'ds4':'runtime reviewed'})
  gpt=next(m for m in plan['models'] if m['model_name']=='gpt-6.1-sol')
  self.assertEqual(gpt['source_group'],'gpt-优秀')
  self.audit['groups']['data']['data']['ds4']['ratio']=.18
  plan=build_plan(self.audit,reviewed_groups={'ds4':'runtime reviewed'})
  gpt=next(m for m in plan['models'] if m['model_name']=='gpt-6.1-sol')
  self.assertEqual(gpt['source_group'],'ds4')
 def test_authenticated_group_permission_is_not_bypassed(self):
  self.audit['groups']['data']['data'].pop('max-gh')
  p={m['model_name']:m for m in build_plan(self.audit)['models']}
  self.assertEqual(p['claude-fable-5-1']['source_group'],'max-codez')
 def test_missing_authenticated_groups_fail_closed(self):
  del self.audit['groups']
  with self.assertRaisesRegex(ValueError,'authenticated'):build_plan(self.audit)
 def test_authenticated_group_ratio_overrides_public(self):
  self.audit['groups']['data']['data']['gpt-优秀']['ratio']=.12
  p={m['model_name']:m for m in build_plan(self.audit)['models']}
  self.assertEqual(p['gpt-6.1-sol']['source_group'],'gpt-稳定')
 def test_cny_markup_once_no_extra_fx(self):
  f=coefficient(.17)
  self.assertAlmostEqual(float(Decimal('2')*f*Decimal('7.3')),.85)
  self.assertAlmostEqual(float(Decimal('10')*f*Decimal('7.3')),4.25)
  self.assertAlmostEqual(float(Decimal('.1')*f*Decimal('7.3')),.0425)
  self.assertAlmostEqual(float(Decimal('2.5')*f*Decimal('7.3')),1.0625)
 def test_expression_keeps_full_formula_and_multiplier(self):
  expr='(len<=272000?tier("base",p*2+cr*.1+cc*2.5+cc1h*4+c*10):tier("long",p*4+c*15))*(param("service_tier")=="ultrafast"?2:1)'
  d,k,w=draft_prices({'billing_mode':'tiered_expr','billing_expr':expr},1.8)
  self.assertTrue(d['billing_setting.billing_expr'].startswith('('+expr+') * '));self.assertEqual(k,'expression');self.assertEqual(w,[])
  self.assertEqual(set(d),{'billing_setting.billing_mode','billing_setting.billing_expr'})
 def test_image_count_and_versioned_expression_preserved(self):
  expr='tier("image",fixed(1))*image_count'
  d,k,w=draft_prices({'billing_mode':'tiered_expr','billing_expr':'v1:'+expr},1)
  self.assertTrue(d['billing_setting.billing_expr'].startswith('tier("image", fixed(0.342465'))
  self.assertTrue(d['billing_setting.billing_expr'].endswith(')) * image_count'))
  self.assertEqual(k,'expression')
 def test_fixed_price_rewrite_is_bounded(self):
  d,_,_=draft_prices({'billing_mode':'tiered_expr','billing_expr':'tier("free",fixed(0))'},1)
  self.assertEqual(d['billing_setting.billing_expr'],'tier("free", fixed(0))')
  for expr in ['tier("a",fixed(p))','tier("a",fixed(1)) * p',
               '(len<1?tier("a",fixed(1)):tier("b",p*2))',
               'tier("a",fixed(1)) * (param("fast")==true?2:1)']:
   with self.assertRaisesRegex(ValueError,'fixed-price'):draft_prices({'billing_mode':'tiered_expr','billing_expr':expr},1)
 def test_legacy_only_base_scaled_cache_preserved(self):
  model={'quota_type':0,'model_ratio':2.5,'completion_ratio':5,'cache_ratio':.1,'create_cache_ratio':1.25,'image_ratio':1,'audio_ratio':2,'audio_completion_ratio':4}
  d,k,w=draft_prices(model,.2)
  self.assertEqual(k,'ratio');self.assertAlmostEqual(d['ModelRatio']*7.3*2,2.5)
  for name,value in [('CompletionRatio',5),('CacheRatio',.1),('CreateCacheRatio',1.25),('AudioRatio',2),('AudioCompletionRatio',4),('ImageRatio',1)]: self.assertEqual(d[name],value)
  self.assertEqual(w,[])
 def test_absent_optional_rates_not_fabricated(self):
  d,k,w=draft_prices({'quota_type':0,'model_ratio':37.5,'completion_ratio':1},.6)
  self.assertNotIn('CacheRatio',d);self.assertNotIn('CreateCacheRatio',d);self.assertEqual(len(w),5)
  self.assertIn('require verification',w[0])
 def test_per_call_and_free_are_preserved(self):
  d,k,w=draft_prices({'quota_type':1,'model_price':.2},1)
  self.assertEqual(k,'per_call');self.assertAlmostEqual(d['ModelPrice']*7.3,.5)
  self.assertNotIn('ModelRatio',d)
  d,_,_=draft_prices({'quota_type':1,'model_price':0},1);self.assertEqual(d['ModelPrice'],0)
 def test_invalid_values_fail_closed(self):
  for bad in [True,-1,float('nan'),float('inf'),None,'bad']:
   with self.assertRaises(Exception):draft_prices({'quota_type':1,'model_price':bad},1)
  for m in [{'billing_mode':'plugin','quota_type':0},{'billing_mode':'tiered_expr','billing_expr':''},{'quota_type':0,'model_ratio':1},{'quota_type':1}]:
   with self.assertRaises(ValueError):draft_prices(m,1)
 def test_snapshot_cas_and_no_overwrite_plugin(self):
  plan=build_plan(self.audit);empty=hashlib.sha256(b'{}').hexdigest();version='a'*64;name=plan['models'][0]['model_name']
  snapshot={'entries':[{'model_name':name,'configured':{},'version':version}],'empty_version':empty}
  patch=attach_snapshot(plan,snapshot);self.assertEqual(patch['changes'][0]['expected_version'],version);self.assertEqual(patch['changes'][1]['expected_version'],empty)
  snapshot['entries'][0]['configured']={'billing_setting.plugin_billing_expr':{'image-plugin':'cost'}}
  with self.assertRaisesRegex(ValueError,'plugin'):attach_snapshot(plan,snapshot)
  with self.assertRaisesRegex(ValueError,'empty_version'):attach_snapshot(plan,{'entries':[]})
 def test_snapshot_rejects_saved_old_luna_models_and_routes(self):
  snapshot={'entries':[],'empty_version':'a'*64}
  for model in [{'model_name':'gpt-5.6-luna','pricing':{}},{'model_name':'GPT-6-LUNA','pricing':{}},{'model_name':'gpt-6.1-sol','source_group':'luna专用','pricing':{}}]:
   with self.subTest(model=model):
    with self.assertRaisesRegex(ValueError,'regenerate the stale plan'):
     attach_snapshot({'models':[model]},snapshot)
if __name__=='__main__':unittest.main()
