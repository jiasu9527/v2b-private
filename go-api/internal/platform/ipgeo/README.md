# Offline client IP geography

This package uses the official ip2region Go reader and pinned public IPv4 / IPv6
xdb files. It never sends client IP addresses to an external service. Only
country / province / city aggregates need to be persisted by callers; the resolver does
not retain addresses or log lookups.

`scripts/prepare-ipgeo.sh` installs the two data files under `storage/ipgeo` (or
`FOREST_IPGEO_DIR`) with fixed SHA256 verification and atomic per-file replacement.
The databases are approximately 11.1 MB and 37.3 MB and are not committed to Git.
Downloads happen during installation / update, not during user requests.
Run the preparation script and restart the backend if initial downloading failed.
Missing databases give an explicit `unavailable` or `partial` status without
preventing the existing service from starting.

Source: <https://github.com/lionsoul2014/ip2region>

Pinned revision: `f8b0792b0cd729f5b2593f690aeed225e49c1967` (2026-10-18).
The upstream project offers `Apache-2.0 OR MIT`; this integration uses the MIT
license reproduced in `LICENSE.ip2region`.

The data format is `country|province|city|ISP|ISO country code`. Location is an IP
network estimate, not a GPS location: VPN, proxy, mobile-network and provider
egress can differ from where the user physically is. Missing province information
does not invent a province and does not discard a valid country.

For China and its regional buckets, `CityName` preserves the city's actual name
from the database only when a province is known. Empty, zero and unknown city
values remain unknown. `NormalizeChinaCityName` removes a trailing 市 or 地区 to
join equivalent names, while retaining full autonomous-prefecture names. It is
a province-local name key, not an official administrative code. Map availability
does not determine whether a city is counted; unmatched names stay in the table.
Municipalities are not extrapolated into districts from city-level evidence.

Country / region codes are ISO 3166-1 alpha-2; China province codes are six-digit
administrative codes. Hong Kong, Macao and Taiwan retain `HK`, `MO`, `TW` in world
aggregates and expose `810000`, `820000`, `710000` for the China province map.
Each user's country bucket and province bucket are counted separately, never
added together. Non-public, invalid and unmapped addresses remain unknown.

Normal unit tests do not download files or need internet access. After preparing
the databases, run a real-data dual-stack smoke test with:

```sh
cd go-api
IPGEO_TEST_DIR="$PWD/../storage/ipgeo" go test ./internal/platform/ipgeo -run TestPreparedDatabases -v
```
