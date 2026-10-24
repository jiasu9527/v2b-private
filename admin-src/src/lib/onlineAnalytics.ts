export type OnlineRegion = { code: string; name: string; users: number; share: number };
export type OnlineCity = OnlineRegion & { province_code: string };

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
