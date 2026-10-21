#!/usr/bin/env python3
"""从不含身份的结果和进程采样计算本次三机对照。"""
import argparse
import json
from pathlib import Path
import statistics


def at(samples, timestamp, key):
    if timestamp < samples[0]["unix_time"] or timestamp > samples[-1]["unix_time"]:
        raise ValueError("请求时间不在采样覆盖范围内")
    for left, right in zip(samples, samples[1:]):
        if left["unix_time"] <= timestamp <= right["unix_time"]:
            span = right["unix_time"] - left["unix_time"]
            return left[key] + (right[key] - left[key]) * (timestamp - left["unix_time"]) / span
    return samples[-1][key]


def interval(rows, started, finished):
    ticks = rows[0]["clock_ticks"]
    samples = [x for x in rows if x.get("event") == "sample"]
    delta = lambda key: at(samples, finished, key) - at(samples, started, key)
    bracket = [x for x in samples if started - .25 <= x["unix_time"] <= finished + .25]
    cpu = (delta("user_ticks") + delta("system_ticks")) / ticks
    host = delta("cpu_ticks")
    return {"cpu_seconds": cpu, "cpu_percent": cpu / (finished - started) * 100,
            "max_rss_bytes": max(x["rss_bytes"] for x in bracket),
            "host_busy_percent": (host - delta("idle_ticks") - delta("steal_ticks")) / host * 100,
            "host_steal_percent": delta("steal_ticks") / host * 100,
            "swap_in_pages": delta("swap_in_pages"), "swap_out_pages": delta("swap_out_pages")}


def summarize(directory):
    hosts = {name: json.loads((directory / (name + ".json")).read_text(encoding="utf-8"))
             for name in ("front", "landing", "client")}
    client = hosts["client"]["files"]
    records = []
    for filename, lines in client.items():
        if not filename.startswith(("r1-", "r2-", "r3-", "s1-", "s2-", "s3-")) or not filename.endswith(".jsonl"):
            continue
        result = next((x for x in lines if x.get("event") == "result"), None)
        if result is None:
            continue
        label = filename.removesuffix(".jsonl")
        group = label.rsplit("-", 2)[0]
        process = client[label + ".process.json"]
        record = {"label": label, "group": group, **result, "valid": result["failed_requests"] == 0}
        for name in ("front", "landing"):
            record[name] = interval(hosts[name]["files"][group + ".monitor.jsonl"],
                                    result["started_unix"], result["finished_unix"])
        before, after = process["host_before"], process["host_after"]
        host_delta = after["cpu_ticks"] - before["cpu_ticks"]
        cpu = process["user_seconds"] + process["system_seconds"]
        record["client"] = {"cpu_seconds": cpu, "cpu_percent": cpu / (process["finished_unix"] - process["started_unix"]) * 100,
                            "max_rss_bytes": process["max_rss_bytes"],
                            "host_steal_percent": (after["steal_ticks"] - before["steal_ticks"]) / host_delta * 100,
                            "swap_in_pages": after["swap_in_pages"] - before["swap_in_pages"],
                            "swap_out_pages": after["swap_out_pages"] - before["swap_out_pages"]}
        records.append(record)
    aggregate = {}
    for prefix, mode in ((prefix, mode) for prefix in ("r", "s")
                         for mode in ("new", "buffered", "realm", "old-ss", "old-vless")):
        items = [x for x in records if x["mode"] == mode and x["label"].startswith(prefix)]
        entry = {"failed_requests": sum(x["failed_requests"] for x in items), "loads": {}}
        groups = sorted({x["group"] for x in items})
        complete_large_groups = [[x for x in items if x["group"] == group and x["requests"] < 128]
                                 for group in groups]
        complete_large_groups = [group for group in complete_large_groups
                                 if len(group) == 4 and all(x["valid"] for x in group)]
        entry["large_payload_resources"] = {
            "successful_groups": len(complete_large_groups),
            "payload_bytes_per_group": sum(x["complete_bytes"] for x in complete_large_groups[0]) if complete_large_groups else 0,
            **{role: {"cpu_seconds_median": statistics.median(sum(x[role]["cpu_seconds"] for x in group)
                                                               for group in complete_large_groups),
                       "max_rss_mib": max(x[role]["max_rss_bytes"] for group in complete_large_groups for x in group) / 1048576}
               for role in ("front", "landing", "client") if complete_large_groups}}
        for name, requests in (("long", 1), ("parallel", 4), ("short", 128)):
            for direction in ("upload", "download"):
                all_items = [x for x in items if x["requests"] == requests and x["direction"] == direction]
                subset = [x for x in all_items if x["valid"]]
                entry["loads"][name + "-" + direction] = {
                    "successful_rounds": len(subset), "attempted_rounds": len(all_items),
                    "mbps_median": statistics.median(x["mbps"] for x in subset) if subset else None,
                    "setup_ms_median": statistics.median(x["setup_ms"]["p50"] for x in subset) if subset else None,
                    "request_ms_median": statistics.median(x["request_ms"]["p50"] for x in subset) if subset else None,
                    **{role: {"cpu_seconds_median": statistics.median(x[role]["cpu_seconds"] for x in subset),
                              "cpu_percent_median": statistics.median(x[role]["cpu_percent"] for x in subset),
                              "max_rss_mib": max(x[role]["max_rss_bytes"] for x in subset) / 1048576}
                       for role in ("front", "landing", "client") if subset}}
        aggregate[prefix + "-" + mode] = entry
    return {"records": sorted(records, key=lambda x: x["label"]), "aggregate": aggregate,
            "cpu_method": "front and landing cumulative ticks interpolated at client request boundaries; client child rusage includes startup and shutdown, sustained series uses per-process wait4",
            "sample_seconds": .2,
            "limitations": ["original subsecond loads make 0.2 second sampling CPU estimates coarse; sustained loads last 4.5 to 6.1 seconds", "RSS sampled on servers; sustained client per-process maximum, earlier series reported cumulative child maximum", "clocks not actively synchronized by experiment", "three single-core hosts and one route", "landing host steal reaches 16.1 percent and existing host has swap activity", "no remote splice trace collected; actual splice verified only in earlier Linux prototype validation"]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = summarize(args.directory)
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(result, output, ensure_ascii=False, indent=2)
