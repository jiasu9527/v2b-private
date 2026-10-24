# Online analytics city maps

These JSON files are served from this application. Opening a province does not
send users' IP addresses, counts, or map requests to any external map service.
Files are loaded individually and their names include a SHA-256 content prefix.

## City boundaries (27 provinces, 363 subdivisions)

Source: [zhChuXiao/ChinaGeoJson](https://github.com/zhChuXiao/ChinaGeoJson), commit
`7aa2c5fabc6a892015625898c3482874b233f9de`, `province/*.json`.

The source repository identifies [Alibaba Cloud DataV.GeoAtlas](https://datav.aliyun.com/portal/school/atlas/area_selector)
as the original map source. Its repository license is MIT, copyright 2024 ChuXiao;
the full notice is retained in `LICENSE-ChinaGeoJson.txt`. This source must not
be described as Natural Earth or geoBoundaries public-domain data.

Modifications: removed unused metadata; kept polygons and administrative codes;
rounded coordinates to four decimal places; added a normalized name for joining
city statistics. A trailing `市` or `地区` is removed from the join key only;
the original Chinese name is retained as `label`. Autonomous prefecture names
retain their full name. No fuzzy-name matching or guessed city coordinates are
applied. The source includes province-administered county-level units as city
peers where relevant (for example in Hainan and Hubei).

This is a pinned map snapshot, not a promise of current administrative boundaries.
Cities not matching this snapshot remain in the statistics table and must not be
assigned to another city or to the entire province.

## Whole-region outlines (7 regions)

Beijing, Tianjin, Shanghai and Chongqing use one whole-municipality polygon.
IP city statistics are not split into districts. Taiwan, Hong Kong and Macao
use whole-region outlines as context; separate city values are shown only in
the table unless matching city geometry is available.

These seven geometries come from the application's existing
`admin-src/src/assets/maps/china-provinces.json`, derived from geoBoundaries gbOpen
`CHN-ADM1-43563684`, commit `9469f09`, boundary year 2019, source
`geoBoundaries, Wikimedia Commons`, license `Public Domain`:

https://github.com/wmgeolab/geoBoundaries/blob/9469f09/releaseData/gbOpen/CHN/ADM1/geoBoundaries-CHN-ADM1_simplified.geojson

## Rebuild and verify

Run `python3 scripts/prepare-online-city-maps.py` from the project checkout.
Use `--check` to verify without changing files. The script downloads only pinned
source URLs and verifies SHA-256 checksums from
`admin-src/src/assets/maps/city-map-sources.json` before generating maps. A local
directory containing the pinned source files may be supplied with `--cache-dir`.
The seven local province outlines are also checksum-checked.

The generated index is `admin-src/src/assets/maps/city-map-index.json`; Vite copies
this directory into `public/assets/admin-new/online-maps` during the normal admin
build. Runtime use does not need Python or a map-provider API key.
