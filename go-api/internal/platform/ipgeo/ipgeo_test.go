package ipgeo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testSearcher struct {
	region string
	err    error
	input  any
	closed bool
}

func (s *testSearcher) Search(ip any) (string, error) {
	s.input = ip
	return s.region, s.err
}

func (s *testSearcher) Close() { s.closed = true }

func TestResolvePublicVersionsAndMappedAddress(t *testing.T) {
	v4 := &testSearcher{region: "中国|广东省|深圳市|电信|CN"}
	v6 := &testSearcher{region: "United States|California|San Jose|Provider|US"}
	db := &Database{v4: v4, v6: v6}
	for _, address := range []string{"113.92.157.29", "::ffff:113.92.157.29", " 113.92.157.29 ", "113.92.157.29:52982", "[::ffff:113.92.157.29]:52982"} {
		got := db.Resolve(address)
		if !got.Known || got.CountryCode != "CN" || got.ProvinceCode != "440000" || got.CityName != "深圳市" || v4.input != "113.92.157.29" {
			t.Fatalf("Resolve(%q)=%+v, reader input=%v", address, got, v4.input)
		}
	}
	for _, address := range []string{"2604:a840:3::a04d", "[2604:a840:3::a04d]", "[2604:a840:3::a04d]:52982"} {
		got := db.Resolve(address)
		if !got.Known || got.CountryCode != "US" || got.CountryName != "美国" || got.ProvinceCode != "" || got.CityName != "" || v6.input != "2604:a840:3::a04d" {
			t.Fatalf("IPv6 result=%+v, reader input=%v", got, v6.input)
		}
	}
}

func TestCityEvidenceRequiresKnownProvinceAndDoesNotInventDistricts(t *testing.T) {
	for _, tc := range []struct{ raw, city string }{
		{"中国|广东省| 深圳市 |电信|CN", "深圳市"},
		{"中国|四川省|阿坝藏族羌族自治州|电信|CN", "阿坝藏族羌族自治州"},
		{"中国|北京市|北京市|移动|CN", "北京市"},
		{"中国|广东省|0|移动|CN", ""},
		{"中国|广东省|未知|移动|CN", ""},
		{"中国|广东省| unknown |移动|CN", ""},
		{"中国|广东省||移动|CN", ""},
		{"中国|0|深圳市|移动|CN", ""},
		{"China|0|台北市|Provider|TW", "台北市"},
		{"China|0|香港|Provider|HK", "香港"},
		{"China|0|0|Provider|MO", ""},
		{"United States|California|San Jose|Provider|US", ""},
	} {
		if got := parseRegion(tc.raw); got.CityName != tc.city {
			t.Errorf("parseRegion(%q) city=%q, want %q", tc.raw, got.CityName, tc.city)
		}
	}
}

func TestNormalizeChinaCityNameRetainsEvidenceWithoutMapLookup(t *testing.T) {
	for _, tc := range []struct{ raw, expected string }{
		{"深圳市", "深圳"}, {" 深圳 ", "深圳"}, {"阿里地区", "阿里"},
		{"阿坝藏族羌族自治州", "阿坝藏族羌族自治州"}, {"锡林郭勒盟", "锡林郭勒盟"},
		{"台北市", "台北"}, {"香港", "香港"}, {"0", ""}, {"unknown", ""}, {"未知", ""},
	} {
		if got := NormalizeChinaCityName(tc.raw); got != tc.expected {
			t.Errorf("NormalizeChinaCityName(%q)=%q, want %q", tc.raw, got, tc.expected)
		}
	}
}

func TestInvalidAndNonPublicAddressesNeverReachDatabase(t *testing.T) {
	s := &testSearcher{region: "中国|广东省|深圳市|电信|CN"}
	db := &Database{v4: s, v6: s}
	for _, address := range []string{"", "hostname.example", "hostname.example:443", "8.8.8.8:http", "8.8.8.8:99999", "1.2.3.4, 8.8.8.8", "2001:4860::1%eth0", "[2001:4860::1%eth0]:443", "https://8.8.8.8"} {
		if got := db.Resolve(address); got.Known || got.UnknownReason != "invalid_ip" {
			t.Errorf("Resolve(%q)=%+v", address, got)
		}
	}
	for _, address := range []string{
		"0.1.2.3", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.1.1",
		"100.64.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "224.0.0.1", "255.255.255.255",
		"::", "::1", "[::1]", "127.0.0.1:443", "[::ffff:10.1.2.3]:443", "::ffff:10.1.2.3", "fc00::1", "fe80::1", "ff02::1", "2001:db8::1", "64:ff9b::808:808", "3fff::1",
	} {
		if got := db.Resolve(address); got.Known || got.UnknownReason != "non_public_ip" {
			t.Errorf("Resolve(%q)=%+v", address, got)
		}
	}
	if s.input != nil {
		t.Fatalf("non-public/invalid address reached database: %v", s.input)
	}
}

func TestRegionDataDoesNotInventLocations(t *testing.T) {
	for _, raw := range []string{"", "0|0|0|0|0", "中国|0|广东|深圳|电信", "China|Guangdong|Shenzhen|0|ZZ", "Earth|0|0|0|001", "China|0|0|0|CCCCC"} {
		if got := parseRegion(raw); got.Known || got.UnknownReason != "not_found" {
			t.Errorf("parseRegion(%q)=%+v", raw, got)
		}
	}
	got := parseRegion("中国|0|0|0|CN")
	if !got.Known || got.CountryCode != "CN" || got.ProvinceCode != "" || got.ProvinceName != "" {
		t.Fatalf("country-only result=%+v", got)
	}
	for _, tc := range []struct{ iso, code, name string }{
		{"HK", "810000", "香港"}, {"MO", "820000", "澳门"}, {"TW", "710000", "台湾"},
	} {
		got := parseRegion("China|0|0|0|" + tc.iso)
		if !got.Known || got.CountryCode != tc.iso || got.ProvinceCode != tc.code || got.ProvinceName != tc.name {
			t.Errorf("region %s=%+v", tc.iso, got)
		}
	}
}

func TestChinaProvinceNamesAndCodes(t *testing.T) {
	for _, tc := range []struct{ raw, code, name string }{
		{"北京市", "110000", "北京"}, {"天津市", "120000", "天津"}, {"河北省", "130000", "河北"},
		{"山西省", "140000", "山西"}, {"内蒙古自治区", "150000", "内蒙古"}, {"辽宁省", "210000", "辽宁"},
		{"吉林省", "220000", "吉林"}, {"黑龙江省", "230000", "黑龙江"}, {"上海市", "310000", "上海"},
		{"江苏省", "320000", "江苏"}, {"浙江省", "330000", "浙江"}, {"安徽省", "340000", "安徽"},
		{"福建省", "350000", "福建"}, {"江西省", "360000", "江西"}, {"山东省", "370000", "山东"},
		{"河南省", "410000", "河南"}, {"湖北省", "420000", "湖北"}, {"湖南省", "430000", "湖南"},
		{"广东省", "440000", "广东"}, {"广西壮族自治区", "450000", "广西"}, {"海南省", "460000", "海南"},
		{"重庆市", "500000", "重庆"}, {"四川省", "510000", "四川"}, {"贵州省", "520000", "贵州"},
		{"云南省", "530000", "云南"}, {"西藏自治区", "540000", "西藏"}, {"陕西省", "610000", "陕西"},
		{"甘肃省", "620000", "甘肃"}, {"青海省", "630000", "青海"}, {"宁夏回族自治区", "640000", "宁夏"},
		{"新疆维吾尔自治区", "650000", "新疆"}, {"台湾省", "710000", "台湾"}, {"香港特别行政区", "810000", "香港"},
		{"澳门特别行政区", "820000", "澳门"}, {"广西", "450000", "广西"}, {"新疆", "650000", "新疆"},
		{"0", "", ""}, {"深圳市", "", ""}, {"未知省", "", ""},
	} {
		code, name := chinaProvince(tc.raw)
		if code != tc.code || name != tc.name {
			t.Errorf("province %q=(%q, %q), want (%q,%q)", tc.raw, code, name, tc.code, tc.name)
		}
	}
}

func TestMissingCorruptAndPartialDatabases(t *testing.T) {
	dir := t.TempDir()
	db := New(dir)
	if got := db.Status(); got.Status != "unavailable" || got.IPv4 || got.IPv6 {
		t.Fatalf("missing status=%+v", got)
	}
	if got := db.Resolve("8.8.8.8"); got.UnknownReason != "database_unavailable" {
		t.Fatalf("missing result=%+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "ip2region_v4.xdb"), []byte("bad download"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := New(dir).Status(); got.Status != "unavailable" {
		t.Fatalf("corrupt status=%+v", got)
	}
	s := &testSearcher{err: errors.New("broken database")}
	db.v4 = s
	db.updateStatus()
	if got := db.Status(); got.Status != "partial" || !got.IPv4 || got.IPv6 {
		t.Fatalf("partial status=%+v", got)
	}
	if got := db.Resolve("8.8.8.8"); got.UnknownReason != "not_found" {
		t.Fatalf("error result=%+v", got)
	}
	if got := db.Resolve("2001:4860:4860::8888"); got.UnknownReason != "database_unavailable" {
		t.Fatalf("missing IPv6 result=%+v", got)
	}
	if err := db.Close(); err != nil || !s.closed || db.Status().Status != "unavailable" {
		t.Fatalf("Close=%v closed=%v status=%+v", err, s.closed, db.Status())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedDatabases(t *testing.T) {
	dir := os.Getenv("IPGEO_TEST_DIR")
	if dir == "" {
		t.Skip("set IPGEO_TEST_DIR to run the optional real-data smoke test")
	}
	db := New(dir)
	defer db.Close()
	if got := db.Status(); got.Status != "ready" {
		t.Fatalf("databases not ready: %+v", got)
	}
	for _, tc := range []struct{ ip, country, province string }{
		{"113.92.157.29", "CN", "440000"},
		{"240e:3b7:3272:d8d0:db09:c067:8d59:539e", "CN", "440000"},
		{"2604:a840:3::a04d", "US", ""},
		{"1.2.3.4", "AU", ""},
	} {
		got := db.Resolve(tc.ip)
		if !got.Known || got.CountryCode != tc.country || got.ProvinceCode != tc.province {
			t.Errorf("real data for %s=%+v, want %s/%s", tc.ip, got, tc.country, tc.province)
		}
		if tc.country == "CN" && got.CityName == "" {
			t.Errorf("real-data Chinese fixture should resolve its city: %+v", got)
		}
		t.Logf("real data geography: %s/%s/%s", got.CountryCode, got.ProvinceCode, got.CityName)
	}
}
