import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Breadcrumb, Button, Empty, Progress, Segmented, Select, Skeleton, Space, Spin, Switch, Table, Tag, Tooltip } from 'antd';
import { ArrowLeftOutlined, ArrowRightOutlined, ClockCircleOutlined, GlobalOutlined, InfoCircleOutlined, ReloadOutlined, TeamOutlined } from '@ant-design/icons';
import * as echarts from 'echarts';
import { apiGet, getAdminPath } from '../lib/api';
import { parseCityMap, provinceCityRows, withinProvinceShare, type CityMap, type OnlineCity, type OnlineRegion } from '../lib/onlineAnalytics';
import worldMap from '../assets/maps/world.json';
import chinaMap from '../assets/maps/china-provinces.json';
import cityMapIndex from '../assets/maps/city-map-index.json';
import './OnlineAnalyticsPage.css';

type Region = OnlineRegion;
type OnlineStats = {
  generated_at: number;
  online_window_seconds: number;
  current_online: number;
  unique_24h: number;
  unique_7d: number;
  total_users: number;
  online_share: number;
  geo_located_users: number;
  geo_unknown_users: number;
  geo_coverage: number;
  countries: Region[];
  china_provinces: Region[];
  china_cities?: OnlineCity[];
  unknown_reasons: { code: string; name: string; users: number }[];
  geo_database: { status: 'ready' | 'partial' | 'unavailable'; ipv4: boolean; ipv6: boolean; source?: string; version?: string };
};
type Scope = 'world' | 'china' | 'province';

const REFRESH_MS = 45_000;
const integer = new Intl.NumberFormat('zh-CN');
const formatCount = (value: number) => integer.format(Number.isFinite(value) ? value : 0);
const percent = (value: number) => `${(Number.isFinite(value) ? value : 0).toFixed(1)}%`;
const mapLabels = {
  world: Object.fromEntries(worldMap.features.map((feature) => [feature.properties.name, feature.properties.label])),
  china: Object.fromEntries(chinaMap.features.map((feature) => [feature.properties.name, feature.properties.label])),
};
const provinceOptions = chinaMap.features.map((feature) => ({ value: feature.properties.name, label: feature.properties.label })).sort((a, b) => a.value.localeCompare(b.value));
const cityMapCache = new Map<string, CityMap>();
const cityMaps: Record<string, { file: string; level: 'city' | 'municipality' | 'region'; note?: string }> = cityMapIndex as any;
const cityMapBase = `${(import.meta as ImportMeta & { env: { BASE_URL: string } }).env.BASE_URL}online-maps/`;

echarts.registerMap('forest-online-world', worldMap as Parameters<typeof echarts.registerMap>[1]);
echarts.registerMap('forest-online-china', chinaMap as Parameters<typeof echarts.registerMap>[1]);

function goDashboard() {
  history.pushState(null, '', `/${getAdminPath()}/dashboard`);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

function Chart({ option, className, onRegionClick, description }: {
  option: echarts.EChartsOption;
  className: string;
  onRegionClick?: (code: string) => void;
  description: string;
}) {
  const element = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts>();
  const click = useRef(onRegionClick);
  click.current = onRegionClick;

  useEffect(() => {
    if (!element.current) return;
    const instance = echarts.init(element.current, undefined, { renderer: 'svg' });
    chart.current = instance;
    instance.on('click', (event: any) => click.current?.(String(event.name || '')));
    const observer = new ResizeObserver(() => instance.resize());
    observer.observe(element.current);
    return () => { observer.disconnect(); instance.dispose(); chart.current = undefined; };
  }, []);
  useEffect(() => { chart.current?.setOption(option, { notMerge: true }); }, [option]);
  return <div ref={element} className={className} role="img" aria-label={description} />;
}

function mapOption(scope: Scope, rows: Region[], labels: Record<string, string>, mapName: string, provinceUsers: number, outlineOnly = false): echarts.EChartsOption {
  const byCode = new Map(rows.filter((row) => row.code).map((row) => [row.code, row]));
  const mappedRows = outlineOnly ? [] : rows.filter((row) => row.code in labels);
  const maximum = Math.max(1, ...mappedRows.map((row) => row.users));
  return {
    animationDuration: 300,
    tooltip: {
      trigger: 'item', renderMode: 'richText', confine: true,
      formatter: (params: any) => {
        const code = String(params.name || '');
        if (outlineOnly) return `${labels[code] || code}\n仅展示地区轮廓，城市人数见下方明细`;
        const row = byCode.get(code);
        return `${labels[code] || row?.name || code}\n当前在线：${formatCount(row?.users || 0)} 人${scope === 'province' ? `\n占本省在线：${percent(withinProvinceShare(row?.users || 0, provinceUsers))}` : ''}\n占全部在线：${percent(row?.share || 0)}${scope === 'world' && code === 'CN' ? '\n点击查看中国省份' : scope === 'china' ? '\n点击查看城市分布' : ''}`;
      },
    },
    visualMap: outlineOnly ? undefined : {
      type: 'continuous', min: 0, max: maximum, calculable: false,
      left: 20, bottom: 16, orient: 'horizontal', itemWidth: 10, itemHeight: 120,
      text: [`${formatCount(maximum)} 人`, '0'], textStyle: { color: '#64748b', fontSize: 11 },
      inRange: { color: ['#dbeafe', '#60a5fa', '#2563eb', '#1e3a8a'] },
      outOfRange: { color: '#edf1f5' },
    },
    series: [{
      name: '当前在线', type: 'map', map: mapName,
      roam: true, scaleLimit: { min: 1, max: 8 }, layoutCenter: ['50%', '47%'], layoutSize: scope === 'world' ? '100%' : '91%',
      nameProperty: 'name', selectedMode: false,
      labelLayout: { hideOverlap: true },
      label: { show: scope !== 'world', formatter: (params: any) => labels[params.name] || '', color: '#475569', fontSize: 10 },
      itemStyle: { areaColor: '#edf1f5', borderColor: '#fff', borderWidth: scope === 'world' ? 0.6 : 1 },
      emphasis: { label: { show: true, formatter: (params: any) => labels[params.name] || '', color: '#0f172a', fontSize: 11 }, itemStyle: { areaColor: '#fbbf24' } },
      data: mappedRows.map((row) => ({ name: row.code, value: row.users })),
    }],
  };
}

function rankOption(rows: Region[]): echarts.EChartsOption {
  const top = rows.slice(0, 8).reverse();
  return {
    animationDuration: 300,
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, renderMode: 'richText', confine: true, formatter: (params: any) => `${params?.[0]?.name || ''}：${formatCount(Number(params?.[0]?.value || 0))} 人` },
    grid: { left: 12, right: 45, top: 8, bottom: 10, containLabel: true },
    xAxis: { type: 'value', minInterval: 1, splitLine: { lineStyle: { color: '#edf1f5' } }, axisLabel: { color: '#94a3b8', fontSize: 10 } },
    yAxis: { type: 'category', data: top.map((row) => row.name), axisLabel: { color: '#475569', width: 86, overflow: 'truncate' }, axisLine: { show: false }, axisTick: { show: false } },
    series: [{ type: 'bar', barMaxWidth: 15, data: top.map((row) => row.users), itemStyle: { color: '#3b82f6', borderRadius: [0, 3, 3, 0] }, label: { show: true, position: 'right', color: '#334155', formatter: (params: any) => formatCount(Number(params.value)) } }],
  };
}

export default function OnlineAnalyticsPage() {
  const [data, setData] = useState<OnlineStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [automatic, setAutomatic] = useState(true);
  const [scope, setScope] = useState<Scope>('world');
  const [provinceCode, setProvinceCode] = useState('440000');
  const [cityMap, setCityMap] = useState<{ provinceCode: string; map: CityMap } | null>(null);
  const [cityMapError, setCityMapError] = useState('');
  const [cityMapLoading, setCityMapLoading] = useState(false);
  const [cityMapRetry, setCityMapRetry] = useState(0);
  const [mapRevision, setMapRevision] = useState(0);
  const mounted = useRef(false);
  const pending = useRef(false);

  const refresh = useCallback(async () => {
    if (pending.current) return;
    pending.current = true;
    setLoading(true);
    try {
      const response = await apiGet('/stat/online');
      if (!response?.data || !Number.isFinite(response.data.current_online)) throw new Error('在线统计返回格式不正确');
      if (mounted.current) { setData(response.data); setError(''); }
    } catch (e: any) {
      if (mounted.current) setError(e?.message || '在线统计加载失败');
    } finally {
      pending.current = false;
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    refresh();
    return () => { mounted.current = false; };
  }, [refresh]);
  useEffect(() => {
    if (!automatic) return;
    const timer = window.setInterval(() => { if (document.visibilityState === 'visible') refresh(); }, REFRESH_MS);
    const onVisible = () => { if (document.visibilityState === 'visible') refresh(); };
    document.addEventListener('visibilitychange', onVisible);
    return () => { window.clearInterval(timer); document.removeEventListener('visibilitychange', onVisible); };
  }, [automatic, refresh]);

  const openProvince = useCallback((code: string) => {
    if (!mapLabels.china[code]) return;
    setProvinceCode(code);
    setScope('province');
  }, []);

  useEffect(() => {
    if (scope !== 'province') return;
    setCityMapError('');
    const cached = cityMapCache.get(provinceCode);
    if (cached) {
      setCityMap({ provinceCode, map: cached });
      setCityMapLoading(false);
      return;
    }
    let active = true;
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 15_000);
    setCityMapLoading(true);
    (async () => {
      try {
        const entry = cityMaps[provinceCode];
        if (!entry?.file) throw new Error('该地区暂未提供城市边界地图');
        const response = await fetch(`${cityMapBase}${entry.file}`, { signal: controller.signal, credentials: 'same-origin' });
        if (!response.ok) throw new Error(response.status === 404 ? '该地区暂未提供城市边界地图' : '城市地图暂时无法加载');
        const value = parseCityMap(await response.json());
        if (!active) return;
        echarts.registerMap(`forest-online-cities-${provinceCode}`, value as Parameters<typeof echarts.registerMap>[1]);
        cityMapCache.set(provinceCode, value);
        setCityMap({ provinceCode, map: value });
      } catch (error: any) {
        if (active) setCityMapError(error?.name === 'AbortError' ? '城市地图加载超时，请重试' : error?.message || '城市地图加载失败');
      } finally {
        window.clearTimeout(timeout);
        if (active) setCityMapLoading(false);
      }
    })();
    return () => { active = false; window.clearTimeout(timeout); controller.abort(); };
  }, [scope, provinceCode, cityMapRetry]);

  const province = data?.china_provinces?.find((row) => row.code === provinceCode);
  const provinceUsers = province?.users || 0;
  const provinceName = province?.name || mapLabels.china[provinceCode] || '所选省份';
  const cityMapInfo = cityMaps[provinceCode];
  const outlineOnly = scope === 'province' && cityMapInfo?.level === 'region';
  const currentCityMap = cityMap?.provinceCode === provinceCode ? cityMap.map : undefined;
  const labels = useMemo(() => scope === 'province' ? Object.fromEntries((currentCityMap?.features || []).map((feature) => [feature.properties.name, feature.properties.label])) : mapLabels[scope], [scope, currentCityMap]);
  const rows = useMemo(() => scope === 'world' ? data?.countries || [] : scope === 'china' ? data?.china_provinces || [] : provinceCityRows(data?.china_cities, provinceCode, provinceUsers, data?.current_online || 0), [scope, data, provinceCode, provinceUsers]);
  const map = useMemo(() => mapOption(scope, rows, labels, scope === 'province' ? `forest-online-cities-${provinceCode}` : `forest-online-${scope}`, provinceUsers, outlineOnly), [scope, rows, labels, provinceCode, provinceUsers, outlineOnly]);
  const rankingRows = useMemo(() => rows.filter((row) => row.code).slice().sort((a, b) => b.users - a.users), [rows]);
  const ranking = useMemo(() => rankOption(rankingRows), [rankingRows]);
  const knownRows = rows.filter((row) => row.code);
  const plottedRows = outlineOnly ? [] : rows.filter((row) => row.code in labels);
  const displayedUsers = rows.reduce((sum, row) => sum + row.users, 0);
  const unmappedUsers = outlineOnly ? displayedUsers : rows.filter((row) => !(row.code in labels)).reduce((sum, row) => sum + row.users, 0);
  const unknownCityUsers = scope === 'province' ? rows.filter((row) => !row.code).reduce((sum, row) => sum + row.users, 0) : 0;
  const cityCoverage = withinProvinceShare(Math.max(0, provinceUsers - unknownCityUsers), provinceUsers);
  const minutes = Math.round((data?.online_window_seconds || 600) / 60);
  const updatedAt = data?.generated_at ? new Date(data.generated_at * 1000).toLocaleString('zh-CN', { hour12: false }) : '';
  const databaseStatus = data?.geo_database?.status;
  const columns = [
    { title: scope === 'world' ? '国家 / 地区' : scope === 'china' ? '省 / 自治区 / 直辖市' : '城市 / 地区', dataIndex: 'name', key: 'name', render: (name: string, row: Region) => scope === 'world' && row.code === 'CN' ? <button type="button" className="online-region-link" onClick={() => setScope('china')}>{name}<ArrowRightOutlined /></button> : scope === 'china' && mapLabels.china[row.code] ? <button type="button" className="online-region-link" onClick={() => openProvince(row.code)}>{name}<ArrowRightOutlined /></button> : <span>{name}{!row.code && <Tag className="online-unknown-tag">未知</Tag>}</span> },
    { title: '在线人数', dataIndex: 'users', key: 'users', align: 'right' as const, sorter: (a: Region, b: Region) => a.users - b.users, render: (value: number) => <strong className="online-table-number">{formatCount(value)}</strong> },
    ...(scope === 'province' ? [{ title: <Tooltip title="分母为所选省级地区当前在线人数，包含城市未知用户。">占本省在线 <InfoCircleOutlined /></Tooltip>, key: 'province_share', align: 'right' as const, render: (_value: unknown, row: Region) => percent(withinProvinceShare(row.users, provinceUsers)) }] : []),
    { title: <Tooltip title="分母为全部当前在线人数，包含未定位用户。">占全部在线 <InfoCircleOutlined /></Tooltip>, dataIndex: 'share', key: 'share', width: 160, render: (value: number) => <div className="online-share-cell"><Progress percent={value} showInfo={false} size="small" strokeColor="#3b82f6" /><span>{percent(value)}</span></div> },
  ];

  return <div className="legacy-page online-page">
    <div className="online-header">
      <Breadcrumb items={[{ title: <button type="button" onClick={goDashboard}>仪表盘</button> }, { title: '在线分析' }]} />
      <div className="online-heading-row">
        <div><h1>在线用户与地区分布</h1><p>了解当前连接分布，查看近 24 小时与 7 天的去重活跃用户。</p></div>
        <Button icon={<ArrowLeftOutlined />} onClick={goDashboard}>返回仪表盘</Button>
      </div>
    </div>
    <div className="online-body">
      <div className="online-toolbar">
        <span className="online-updated"><span className={`online-status-dot${error ? ' is-stale' : ''}`} />{updatedAt ? `数据更新于 ${updatedAt}` : '正在获取统计数据'}{data && error && ' · 当前显示上次成功数据'}</span>
        <Space size={12} wrap><span className="online-auto"><Switch size="small" checked={automatic} onChange={setAutomatic} aria-label="每45秒自动刷新" />45 秒自动刷新</span><Button icon={<ReloadOutlined spin={loading} />} onClick={refresh} disabled={loading}>刷新</Button></Space>
      </div>
      {error && <Alert type="error" showIcon message={data ? '刷新失败，已保留上次数据' : '暂时无法获取在线统计'} description={error} action={<Button size="small" onClick={refresh} disabled={loading}>重试</Button>} />}
      {!data && loading && <div className="online-panel online-loading"><Skeleton active paragraph={{ rows: 7 }} /></div>}
      {!data && !loading && <div className="online-panel online-loading"><Empty description="统计数据暂不可用，请重试" /></div>}
      {data && <>
        <div className="online-stat-grid">
          <Metric title="当前在线" value={data.current_online} unit="人" note={`近 ${minutes} 分钟有流量活动`} icon={<TeamOutlined />} primary />
          <Metric title="24 小时内在线" value={data.unique_24h} unit="人" note="过去 24 小时 · 用户去重" icon={<ClockCircleOutlined />} />
          <Metric title="7 天内在线" value={data.unique_7d} unit="人" note="过去 7 天 · 包含近 24 小时" icon={<ClockCircleOutlined />} />
          <Metric title="当前在线占比" value={percent(data.online_share)} note={`全部注册用户 ${formatCount(data.total_users)} 人`} icon={<GlobalOutlined />} />
        </div>

        <div className="online-geography-grid">
          <section className="online-panel online-map-panel">
            <div className="online-panel-header"><div><h2>{scope === 'province' ? `${provinceName} · 城市分布` : '在线地区分布'}</h2><p>每位用户归属最新上报的一个地区</p></div><Segmented aria-label="地图范围" value={scope === 'province' ? 'china' : scope} options={[{ label: '世界', value: 'world' }, { label: '中国省份', value: 'china' }]} onChange={(value) => setScope(value as Scope)} /></div>
            {scope !== 'world' && <div className="online-province-navigation"><Space size={8} wrap>{scope === 'province' && <Button size="small" icon={<ArrowLeftOutlined />} onClick={() => setScope('china')}>返回全国</Button>}<Select aria-label="选择省份查看城市分布" placeholder="选择省份查看城市" value={scope === 'province' ? provinceCode : undefined} options={provinceOptions} onChange={openProvince} showSearch optionFilterProp="label" className="online-province-select" /></Space><span>{scope === 'china' ? '点击地图或省份名称，查看城市分布' : '切换省份可继续查看其他地区'}</span></div>}
            {scope === 'province' && <><div className="online-province-stats"><div><span>本省在线</span><strong>{formatCount(provinceUsers)}<small>人</small></strong></div><div><span>已识别城市</span><strong>{formatCount(Math.max(0, provinceUsers - unknownCityUsers))}<small>人 · {percent(cityCoverage)}</small></strong></div><div><span>城市未知</span><strong>{formatCount(unknownCityUsers)}<small>人</small></strong></div></div>{(!data.china_cities || unknownCityUsers > 0 || cityMapInfo?.note) && <p className="online-city-note">{cityMapInfo?.note ? `${cityMapInfo.note} ` : ''}{!data.china_cities ? '城市数据尚未返回，后端更新并收到新的节点上报后会逐步显示。' : unknownCityUsers > 0 ? '仅识别到省份或旧版上报的用户列为城市未知，新上报后会逐步更新。' : ''}</p>}</>}
            <div className="online-map-meta"><span><strong>{formatCount(knownRows.length)}</strong> 个{scope === 'world' ? '国家 / 地区' : scope === 'china' ? '省级地区' : '城市 / 地区'}有在线用户</span><Button size="small" type="text" onClick={() => setMapRevision((value) => value + 1)}>重置视图</Button></div>
            <div className="online-map-shell">
              {scope === 'province' && !currentCityMap ? <div className="online-city-map-status">{cityMapLoading || !cityMapError ? <><Spin /><strong>正在加载{provinceName}城市地图</strong></> : <><GlobalOutlined /><strong>{cityMapError}</strong><span>下方城市明细和排行仍可查看</span><Button size="small" icon={<ReloadOutlined />} onClick={() => setCityMapRetry((value) => value + 1)}>重新加载地图</Button></>}</div> : <><Chart key={`${scope}-${provinceCode}-${mapRevision}`} option={map} className="online-map-chart" onRegionClick={(code) => { if (scope === 'world' && code === 'CN') setScope('china'); else if (scope === 'china') openProvince(code); }} description={`${scope === 'world' ? '世界' : scope === 'china' ? '中国省份' : `${provinceName}城市`}当前在线人数地图，详细数据见下方地区明细`} />
              {!plottedRows.length && !outlineOnly && <div className="online-map-empty"><GlobalOutlined /><strong>{data.current_online === 0 ? '当前没有在线用户' : knownRows.length ? '当前在线地区请见明细表' : scope === 'china' ? '暂无可展示的中国省份数据' : scope === 'province' ? provinceUsers > 0 ? '暂无可定位的城市数据' : '该省当前没有在线用户' : '暂无可定位的在线用户'}</strong><span>{data.current_online === 0 || (scope === 'province' && !provinceUsers) ? '新的流量活动上报后会自动更新' : knownRows.length ? '底图暂未覆盖这些地区' : '缺少位置的用户仍计入在线总人数'}</span></div>}</>}
            </div>
            <div className="online-map-footnote"><span>拖动或滚轮缩放{scope === 'world' ? ' · 点击中国查看省份' : scope === 'china' ? ' · 点击省份查看城市' : ''}</span><span>{outlineOnly ? '仅展示地区轮廓，城市人数见明细' : `灰色：未记录到在线用户${unmappedUsers > 0 ? ` · ${formatCount(unmappedUsers)} 人仅在表格中展示` : ''}`}</span></div>
          </section>

          <aside className="online-panel online-location-panel">
            <div className="online-panel-header"><div><h2>定位覆盖</h2><p>当前在线用户的地区识别情况</p></div></div>
            <div className="online-coverage"><div><strong>{percent(data.geo_coverage)}</strong><span>已识别国家 / 地区</span></div><Progress type="circle" percent={data.geo_coverage} size={72} showInfo={false} strokeWidth={8} strokeColor="#3b82f6" /></div>
            <div className="online-location-counts"><div><span><i className="online-legend-dot located" />已定位</span><strong>{formatCount(data.geo_located_users)} <small>人</small></strong></div><div><span><i className="online-legend-dot" />位置未知</span><strong>{formatCount(data.geo_unknown_users)} <small>人</small></strong></div></div>
            {data.geo_unknown_users > 0 && <div className="online-unknown-reasons"><h3>未知位置说明</h3>{(data.unknown_reasons || []).filter((item) => item.users > 0).map((item) => <div key={item.code}><span>{item.name}</span><strong>{formatCount(item.users)}</strong></div>)}<p>未知位置不分配到地图，在线总人数不受影响。</p></div>}
            <div className="online-db-status"><span>地区数据库</span><Tag color={databaseStatus === 'ready' ? 'green' : databaseStatus === 'partial' ? 'gold' : 'default'}>{databaseStatus === 'ready' ? '已就绪' : databaseStatus === 'partial' ? '部分可用' : '未就绪'}</Tag></div>
            {databaseStatus !== 'ready' && <p className="online-db-note">{databaseStatus === 'partial' ? '部分 IP 类型暂时无法定位，已单独计入未知位置。' : '地区数据库尚未就绪；准备完成并收到新的节点上报后，可刷新查看分布。'}</p>}
          </aside>
        </div>

        <div className="online-details-grid">
          <section className="online-panel online-table-panel"><div className="online-panel-header"><div><h2>{scope === 'world' ? '国家与地区明细' : scope === 'china' ? '中国省份明细' : `${provinceName}城市明细`}</h2><p>{formatCount(rows.length)} 个统计地区 · 合计 {formatCount(displayedUsers)} 人{scope === 'world' && data.geo_unknown_users > 0 ? `，另有 ${formatCount(data.geo_unknown_users)} 人位置未知` : ''}</p></div></div><Table key={`${scope}-${provinceCode}`} rowKey={(row) => row.code || 'unknown'} columns={columns} dataSource={rows} size="middle" pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }} scroll={{ x: scope === 'province' ? 580 : 480 }} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无地区数据" /> }} /></section>
          <section className="online-panel online-rank-panel"><div className="online-panel-header"><div><h2>{scope === 'province' ? `${provinceName}城市排行` : '在线地区排行'}</h2><p>{scope === 'world' ? '国家 / 地区' : scope === 'china' ? '省级地区' : '城市 / 地区'} · 前 8 名{scope === 'province' ? ' · 不含城市未知' : ''}</p></div></div>{rankingRows.length > 0 ? <Chart option={ranking} className="online-rank-chart" description="在线人数前八名条形图，详细人数见地区明细表" /> : <div className="online-rank-empty"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无排行数据" /></div>}</section>
        </div>
        <section className="online-method"><h3><InfoCircleOutlined /> 统计口径</h3><p>当前在线指近 {minutes} 分钟有节点流量活动的用户，不等同于客户端连接数。24 小时与 7 天人数按用户 ID 去重，7 天包含近 24 小时，同一用户连接多个节点只计一人。</p><p>地区仅统计当前在线用户，根据节点最近上报的用户 IP 推断；多设备、多地区以最新一次上报为准。未上报 IP、上报过期或无法识别的用户计入未知，不使用节点服务器所在地替代。</p><p>城市定位为 IP 近似结果，可能对应运营商出口，并非用户的实际位置。直辖市按整市统计，不继续划分区县；无法识别到城市的用户单列未知，小地区或缺少边界的城市仅在明细中显示。本省占比包含本省城市未知用户，全部在线占比包含所有未定位用户。</p><p>底图：Natural Earth / geoBoundaries（公共领域）、ChinaGeoJson（MIT）。</p></section>
      </>}
    </div>
  </div>;
}

function Metric({ title, value, unit, note, icon, primary = false }: { title: string; value: number | string; unit?: string; note: string; icon: React.ReactNode; primary?: boolean }) {
  return <section className={`online-metric${primary ? ' is-primary' : ''}`}><div className="online-metric-title"><span>{title}</span>{icon}</div><div className="online-metric-value">{typeof value === 'number' ? formatCount(value) : value}{unit && <small>{unit}</small>}</div><p>{note}</p></section>;
}
