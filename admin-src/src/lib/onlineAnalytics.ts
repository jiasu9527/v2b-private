export type OnlineRegion = { code: string; name: string; users: number; share: number };
export type OnlineCity = OnlineRegion & { province_code: string };

export type OnlineUsersSelection = { title: string } & (
  { scope: 'all' } |
  { scope: 'country'; country_code: string } |
  { scope: 'province'; province_code: string } |
  { scope: 'city'; province_code: string; city: string } |
  { scope: 'unknown'; reason?: string }
);

export type OnlineUser = {
  id: number;
  email: string;
  last_seen_at: number;
  location_seen_at: number;
  country_code: string;
  country_name: string;
  province_code: string;
  province_name: string;
  city_name: string;
  unknown_reason: string;
};

export type OnlineUsersResult = {
  total: number;
  page: number;
  page_size: number;
  generated_at: number;
  online_window_seconds: number;
  users: OnlineUser[];
};

// An explicit empty city or province selects unresolved observations. Preserve
// those query values instead of accidentally widening the user list.
export function onlineUsersParams(selection: OnlineUsersSelection, page: number, pageSize: number, search = '') {
  const { title: _title, ...filter } = selection;
  return { ...filter, page, page_size: pageSize, ...(search.trim() ? { search: search.trim() } : {}) };
}

const onlineUnknownNames: Record<string, string> = {
  not_reported: '节点尚未上报用户地址',
  location_stale: '旧版后端将位置判为过期，请更新后端',
  invalid_timestamp: '位置上报时间异常',
  database_unavailable: '离线定位库未就绪',
  not_found: '未匹配到地区',
  invalid_ip: '地址格式无法识别',
  non_public_ip: '内网或保留地址',
};

export function onlineUserLocation(user: OnlineUser): string {
  if (user.unknown_reason) return '位置未知';
  const names = [user.country_name || user.country_code, user.province_name, user.city_name].filter(Boolean);
  return [...new Set(names)].join(' · ') || '位置未知';
}

export function onlineUserLocationNote(user: OnlineUser, reasons: Record<string, string> = {}): string {
  if (user.unknown_reason) return reasons[user.unknown_reason] || onlineUnknownNames[user.unknown_reason] || '未匹配到地区';
  if (['CN', 'HK', 'MO', 'TW'].includes(user.country_code)) {
    if (!user.province_code) return '已识别国家 / 地区，省份未知';
    if (!user.city_name) return '已识别省份，城市未知';
  }
  return '';
}

export function withinProvinceShare(users: number, provinceUsers: number): number {
  return provinceUsers > 0 ? Math.min(100, users / provinceUsers * 100) : 0;
}

// Older observations only contain a province. Keep these users visible as
// unknown cities rather than assigning them to the provincial capital.
export function provinceCityRows(cities: OnlineCity[] | undefined, provinceCode: string, provinceUsers: number, currentOnline: number): OnlineRegion[] {
  const rows = (cities || []).filter((city) => city.province_code === provinceCode).map(({ code, name, users, share }) => ({ code, name, users, share }));
  const reportedUsers = rows.reduce((sum, row) => sum + row.users, 0);
  const missingUsers = Math.max(0, provinceUsers - reportedUsers);
  if (missingUsers > 0) {
    const unknown = rows.find((row) => !row.code);
    if (unknown) {
      unknown.users += missingUsers;
      unknown.share = currentOnline > 0 ? unknown.users / currentOnline * 100 : 0;
    } else {
      rows.push({ code: '', name: '城市未知', users: missingUsers, share: currentOnline > 0 ? missingUsers / currentOnline * 100 : 0 });
    }
  }
  return rows.sort((a, b) => b.users - a.users || a.code.localeCompare(b.code));
}

export type CityMap = {
  type: 'FeatureCollection';
  features: { type: 'Feature'; properties: { name: string; label: string }; geometry: { type: string; coordinates: unknown[] } }[];
};

export function parseCityMap(value: unknown): CityMap {
  const map = value as CityMap;
  if (!map || map.type !== 'FeatureCollection' || !Array.isArray(map.features) || !map.features.length || map.features.some((feature) => (
    feature?.type !== 'Feature' || typeof feature.properties?.name !== 'string' || !feature.properties.name ||
    typeof feature.properties?.label !== 'string' || !feature.properties.label ||
    !['Polygon', 'MultiPolygon'].includes(feature.geometry?.type) || !Array.isArray(feature.geometry.coordinates)
  ))) throw new Error('城市地图格式不正确');
  return map;
}
