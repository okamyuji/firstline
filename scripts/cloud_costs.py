"""Monthly totals per stage and provider for ADR-0006: Tokyo, on-demand, USD, 730 hours a month.
Unit prices are the values the providers' price APIs and price pages gave on 2026-11-27."""

H = 730  # hours per month used by the price pages
GB_PER_GIB = 1.073741824

P = {
    "aws": dict(vm2=0.0216, vm4=0.0432, ip=0.005, disk=0.096, obj=0.025, put=0.0000047,
                egress_free_gb=100, egress=0.114,
                db_s=0.025, db_ha=0.05, dbst_s=0.138, dbst_ha=0.276,
                lb_h=0.0243, lcu=0.008, fg_vcpu=0.04045, fg_gb=0.00442),
    "google": dict(vm2=0.02149143, vm4=0.04298286, ip=0.005, disk=0.13, obj=0.023, put=0.000005,
                   egress_free_gib=200, egress=0.11,  # Standard Tier from Tokyo
                   db_s=0.014, db_ha=0.0273, dbst_s=0.221, dbst_ha=0.442),
    "azure": dict(vm2=0.0272, vm4=0.0432, ip=0.005, disk_fixed32=2.4, disk_fixed128=9.6, obj=0.02, put=0.000005,
                  egress_free_gb=100, egress=0.11,  # Routing Preference: Internet
                  db_s=0.026, dbst_s=0.138),
}


def egress(p, gb):
    if "egress_free_gib" in p:
        return max(0.0, gb / GB_PER_GIB - p["egress_free_gib"]) * p["egress"]
    return max(0.0, gb - p["egress_free_gb"]) * p["egress"]


def disk(name, p, gb):
    if name == "azure":
        return p["disk_fixed32"] if gb <= 32 else p["disk_fixed128"]
    return gb * p["disk"]


def stage1(name, p):  # one 2 GiB VM, 20 GB disk, 1 IPv4, 1 GB in the bucket
    return dict(vm=p["vm2"] * H, ip=p["ip"] * H, disk=disk(name, p, 20), obj=p["obj"] * 1)


def stage1_4gib(name, p):  # one 4 GiB VM, 100 GB disk
    return dict(vm=p["vm4"] * H, ip=p["ip"] * H, disk=disk(name, p, 100), obj=p["obj"] * 5)


def stage2_db(name, p, ha):  # one 2 GiB app VM, smallest managed PostgreSQL, 20 GB DB storage
    if ha and "db_ha" not in p:
        return None
    d = dict(vm=p["vm2"] * H, ip=p["ip"] * H, disk=disk(name, p, 20))
    d["db"] = (p["db_ha"] if ha else p["db_s"]) * H
    d["dbst"] = (p["dbst_ha"] if ha else p["dbst_s"]) * 20
    return d


def stage3_aws(p):  # ALB in 2 AZs, 2 web tasks (1 vCPU, 2 GB) and 1 jobs task on Fargate ARM, Multi-AZ db.t4g.micro
    return dict(alb=p["lb_h"] * H, alb_ip=2 * p["ip"] * H, lcu=1 * p["lcu"] * H,
                tasks=2 * (p["fg_vcpu"] + 2 * p["fg_gb"]) * H,
                jobs_task=(0.25 * p["fg_vcpu"] + 0.5 * p["fg_gb"]) * H, task_ip=3 * p["ip"] * H,
                db=p["db_ha"] * H, dbst=p["dbst_ha"] * 20)


def fmt(d):
    return "" if d is None else f"{sum(d.values()):.2f}"


def items(d):
    return "{" + ", ".join(f"{k}: {v:.2f}" for k, v in d.items()) + "}"


rows = []
for name, p in P.items():
    s3s, s3h = stage2_db(name, p, False), stage2_db(name, p, True)
    rows.append((name, fmt(stage1(name, p)), fmt(stage1_4gib(name, p)), fmt(s3s), fmt(s3h),
                 *(f"{egress(p, gb):.2f}" for gb in (50, 500, 2000))))
print("| provider | stage 1 | stage 1 at 4 GiB | stage 2 single | stage 2 HA | egress 50GB | egress 500GB | egress 2000GB |")
print("|---|---|---|---|---|---|---|---|")
for r in rows:
    print("| " + " | ".join(r) + " |")
s3 = stage3_aws(P["aws"])
print("\nAWS stage 3 items:", items(s3), "total", fmt(s3))
for ha in (False, True):
    print("AWS stage 2", "HA" if ha else "single", "items:", items(stage2_db("aws", P["aws"], ha)),
          "app host", f"{P['aws']['vm2'] * H + P['aws']['ip'] * H + disk('aws', P['aws'], 20):.2f}")
for name, p in P.items():
    print(name, "stage 1 items", items(stage1(name, p)))
t = {n: (stage1_4gib(n, p), stage2_db(n, p, True), [egress(p, gb) for gb in (500, 2000)]) for n, p in P.items()}
print("AWS minus Google: egress 500GB", f"{t['aws'][2][0] - t['google'][2][0]:.2f}",
      "egress 2000GB", f"{t['aws'][2][1] - t['google'][2][1]:.2f}",
      "stage 2 HA", f"{sum(t['aws'][1].values()) - sum(t['google'][1].values()):.2f}")
print("AWS minus Azure at 4 GiB:", f"{sum(t['aws'][0].values()) - sum(t['azure'][0].values()):.2f}")
# Custom metrics: 10 EMF metrics, the error metric filter and the agent's disk metric; 10 are free.
print("CloudWatch metrics over the free 10:", f"{(10 + 1 + 1 - 10) * 0.30:.2f}")
# Litestream PUTs one L0 file per second of continuous writes, plus L1 every 30 s, L2 every 5 min,
# L3 every hour and a daily snapshot.
puts = 3600 * H + 120 * H + 12 * H + H + 30
print("Litestream PUT bound, continuous writes, 730 h:", f"{puts * P['aws']['put']:.2f}")
# Measured with Litestream v0.5.18 while writing once a second: L0 listed 480 times and L1 375 times an hour.
print("Litestream LIST while writing, 730 h:", f"{(480 + 375) * H * P['aws']['put']:.2f}")
# Versioning keeps every file Litestream deletes for 30 days. Measured on a 21 MB database written once a
# second: L0 6,272 B a second, L1 123,481 B every 30 s, L2 980,784 B every 5 min.
ltx_gb = (6272 * 3600 + 123481 * 120 + 980784 * 12) * 24 * 30 / 1e9
print("Old versions: daily snapshots of a 1 GB database for 30 days", f"{30 * 1 * P['aws']['obj']:.2f}",
      "; deleted LTX files of 30 days of writes once a second", f"{ltx_gb:.1f} GB", f"{ltx_gb * P['aws']['obj']:.2f}")
ep = 4 * 2 * 0.014 * H  # ECR api, ECR dkr, CloudWatch Logs, ssmmessages in 2 AZs
ip = P["aws"]["ip"] * H
n = next(n for n in range(1, 100) if n * ip > ep)
print("interface endpoints:", f"{ep:.2f}", "public IPv4 cheaper up to", n - 1, "tasks:", f"{(n - 1) * ip:.2f}")
