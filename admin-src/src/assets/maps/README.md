# Online analytics map data

These simplified geographic boundaries are bundled locally. The browser does not
download maps from a third-party CDN or send user data to a map service.

## World

- Source: Natural Earth, `ne_110m_admin_0_countries.geojson`, downloaded 2026-10-24.
- URL: https://github.com/nvkelso/natural-earth-vector/blob/master/geojson/ne_110m_admin_0_countries.geojson
- License: Public domain. https://www.naturalearthdata.com/about/terms-of-use/
- Original SHA-256: `6866c877d39cba9c357620878839b336d569f8c662d3cfab4cb1dbe2d39c977f`
- Derivation: retain geometry and ISO alpha-2 / Chinese display names, round
  coordinates to four decimals, omit Antarctica. Kosovo uses `XK`; Northern Cyprus
  and Somaliland have separate internal geometry names to avoid duplicate keys.
- At 1:110m scale some small countries/regions have no visible polygon. Their
  statistics remain available in the distribution table.

## China provinces

- Source: geoBoundaries gbOpen, `CHN-ADM1-43563684`, simplified geometry, commit
  `9469f09`. Its metadata lists 34 administrative units, boundaries represented in
  2019, boundary source `geoBoundaries, Wikimedia Commons`, license `Public Domain`.
- Metadata: https://www.geoboundaries.org/api/current/gbOpen/CHN/ADM1/
- Source: https://github.com/wmgeolab/geoBoundaries/blob/9469f09/releaseData/gbOpen/CHN/ADM1/geoBoundaries-CHN-ADM1_simplified.geojson
- Original SHA-256: `bc4afc7eacf4351ae5b3ae7a612327987ce1123cb5deb8574fb49107091c6623`
- Derivation: retain geometry, round coordinates to four decimals, replace source
  labels with Chinese province labels and six-digit administrative region codes.
  The source label `Guangzhou Province` is normalized to Guangdong (`440000`).

Maps communicate approximate IP-derived regional statistics, not precise user
positions or a statement about boundary status. Unknown locations are never
assigned to a map polygon.
# City drill-down maps

Province drill-down resources are loaded individually from
`admin-src/public/online-maps/`. See that directory's `README.md` for provenance,
MIT attribution, whole-region exceptions and reproducible generation.
`city-map-index.json` is the small importable index; map geometry is not imported
into the React bundle. `city-map-sources.json` pins every upstream input checksum.
