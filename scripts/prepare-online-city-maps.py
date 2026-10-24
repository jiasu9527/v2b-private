#!/usr/bin/env python3
"""Rebuild pinned, local city maps; no runtime requests to a map provider."""

import argparse
import hashlib
import json
from pathlib import Path
from urllib.parse import quote
from urllib.request import urlopen


ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "admin-src/src/assets/maps"
OUTPUT = ROOT / "admin-src/public/online-maps"
LOCK = ASSETS / "city-map-sources.json"
SINGLE_REGIONS = {
    "110000": ("北京", "municipality"),
    "120000": ("天津", "municipality"),
    "310000": ("上海", "municipality"),
    "500000": ("重庆", "municipality"),
    "710000": ("台湾", "region"),
    "810000": ("香港", "region"),
    "820000": ("澳门", "region"),
}


def normalize_name(name):
    name = name.strip()
    for suffix in ("地区", "市"):
        if name.endswith(suffix):
            return name[:-len(suffix)]
    return name


def compact_bytes(value):
    return (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode()


def round_coordinates(value):
    if isinstance(value, list):
        return [round_coordinates(child) for child in value]
    return round(value, 4)


def verified_download(url, checksum, cached_path):
    raw = cached_path.read_bytes() if cached_path and cached_path.exists() else urlopen(url, timeout=45).read()
    actual = hashlib.sha256(raw).hexdigest()
    if actual != checksum:
        raise ValueError("SHA256 mismatch for " + url + ": " + actual)
    return raw


def build(cache_dir=None, check=False):
    lock = json.loads(LOCK.read_text())
    base = "https://raw.githubusercontent.com/zhChuXiao/ChinaGeoJson/" + lock["revision"] + "/"
    base_provinces_raw = (ASSETS / "china-provinces.json").read_bytes()
    if hashlib.sha256(base_provinces_raw).hexdigest() != lock["single_region_geometry_sha256"]:
        raise ValueError("Province geometry changed; inspect and explicitly refresh city-map-sources.json")
    base_provinces = {feature["properties"]["name"]: feature for feature in json.loads(base_provinces_raw)["features"]}
    generated = {}
    index = {}
    for code in sorted(set(lock["provinces"]) | set(SINGLE_REGIONS)):
        if code in SINGLE_REGIONS:
            label, level = SINGLE_REGIONS[code]
            features = [{"type": "Feature", "properties": {"name": label, "label": label, "adcode": code}, "geometry": base_provinces[code]["geometry"]}]
            note = "直辖市按全市汇总，IP 定位不拆分到区县。" if level == "municipality" else "底图仅提供地区轮廓；可识别城市显示在下方明细，不将城市人数涂到整个地区。"
        else:
            source = lock["provinces"][code]
            cache = cache_dir / (code + ".json") if cache_dir else None
            original = json.loads(verified_download(base + quote(source["source_path"]), source["sha256"], cache))
            features = []
            for feature in original["features"]:
                properties = feature["properties"]
                if properties["level"] != "city":
                    raise ValueError("Unexpected non-city subdivision in " + code)
                label = properties["name"]
                features.append({"type": "Feature", "properties": {"name": normalize_name(label), "label": label, "adcode": str(properties["adcode"])}, "geometry": {"type": feature["geometry"]["type"], "coordinates": round_coordinates(feature["geometry"]["coordinates"])}})
            if len(features) != source["features"]:
                raise ValueError("Unexpected city count in " + code)
            if len({feature["properties"]["name"] for feature in features}) != len(features):
                raise ValueError("Ambiguous normalized city names in " + code)
            level = "city"
            note = "未匹配底图的城市保留在明细中。"
            if any(not feature["properties"]["adcode"].endswith("00") for feature in features):
                note = "含省直辖县级行政区；" + note
        content = compact_bytes({"type": "FeatureCollection", "features": features})
        filename = code + "." + hashlib.sha256(content).hexdigest()[:12] + ".json"
        generated[OUTPUT / filename] = content
        index[code] = {"file": filename, "level": level, "note": note}
    license_cache = cache_dir / "LICENSE" if cache_dir else None
    generated[OUTPUT / "LICENSE-ChinaGeoJson.txt"] = verified_download(base + "LICENSE", lock["license_sha256"], license_cache)
    generated[ASSETS / "city-map-index.json"] = (json.dumps(index, ensure_ascii=False, indent=2) + "\n").encode()
    for destination, content in generated.items():
        if check:
            if not destination.exists() or destination.read_bytes() != content:
                raise ValueError("Generated asset is not current: " + str(destination))
        else:
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(content)
    # Only remove previously generated files for these exact province codes.
    if not check:
        for code in index:
            for old in OUTPUT.glob(code + ".*.json"):
                if old not in generated:
                    old.unlink()
    print(str(len(index)) + " province maps validated" + ("" if check else " and written"))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cache-dir", type=Path, help="Optional directory of pinned {province_code}.json files and LICENSE")
    parser.add_argument("--check", action="store_true", help="Verify generated files without writing")
    arguments = parser.parse_args()
    build(arguments.cache_dir, arguments.check)
