# Public Service Technical Verification

Date: 2026-10-19. Executed from the Windows development machine using curl 8.13.0 / Schannel and PowerShell HttpClient, with HTTPS certificate verification enabled. curl `--noproxy "*"` and HttpClient `UseProxy = false` disabled explicit client proxies; they do not establish the underlying network route.

## Observed results

| Request | Observed result | Single-request duration |
| --- | --- | --- |
| GET `https://api.ipify.org?format=json` | 200, 23 bytes, valid IPv4 JSON | 0.203 s |
| GET `https://api64.ipify.org?format=json` | 200, 23 bytes, same IPv4 | 1.241 s |
| GET `https://checkip.amazonaws.com/` | 200, plain text containing the same IPv4 | 0.385 s |
| GET `https://speed.cloudflare.com/__down?bytes=1048576` | 200, exactly 1,048,576 bytes | 0.215 s |
| GET `https://speed.cloudflare.com/__down?bytes=100000` | 200, exactly 100,000 bytes; matching Content-Length | 0.139 s |
| POST `https://speed.cloudflare.com/__up`, 1,048,576 random bytes | 200; `cf-meta-upload-bytes: 1048576` | 0.255 s |
| GET `https://api.ipapi.is/?q=1.1.1.1` | 200, 284-byte anonymous geo record | 0.296 s |
| POST `https://api.ipapi.is/`, JSON `{"q":"1.1.1.1"}` | 200, same anonymous geo record | 0.191 s |
| GET `https://api.ipapi.is/?q=not-an-ip` | 400, `ERR_INVALID_IP_OR_ASN` | Not measured |

The development machine's public IP is deliberately omitted. The anonymous 1.1.1.1 record included `country: Australia`, `city: Brisbane`, and `asn: AS13335 Cloudflare, Inc.`. This is an anycast address: the probe validates response shape, not physical-location accuracy.

Download returned `Cache-Control: no-store, no-transform`, `Content-Type: application/octet-stream`, and `Server-Timing`. Upload returned an empty body, `Server-Timing`, and the accepted-byte header. Do not expect JSON from these speed endpoints.

Speed probes transferred 1,148,576 download bytes and 1,048,576 upload bytes: approximately 2.10 MiB of payload in total. Small probes verify protocol compatibility, not maximum bandwidth. Some independent probes ran concurrently, so these timings are not benchmark results.

## Reproduction

PowerShell commands, without creating payload files:

```powershell
curl.exe --noproxy "*" --max-time 25 --silent --show-error --write-out "\nHTTP=%{http_code}\n" "https://api64.ipify.org?format=json"
curl.exe --noproxy "*" --max-time 25 --silent --show-error "https://checkip.amazonaws.com/"
curl.exe --noproxy "*" --max-time 25 --silent --show-error --output NUL --write-out "HTTP=%{http_code} bytes=%{size_download} time=%{time_total}\n" "https://speed.cloudflare.com/__down?bytes=1048576"
curl.exe --noproxy "*" --max-time 25 --silent --show-error --header "Content-Type: application/json" --data-raw '{"q":"1.1.1.1"}' "https://api.ipapi.is/"
```

Exact-size upload:

```powershell
$handler = [System.Net.Http.HttpClientHandler]::new()
$handler.UseProxy = $false
$client = [System.Net.Http.HttpClient]::new($handler)
$client.Timeout = [TimeSpan]::FromSeconds(30)
$payload = [byte[]]::new(1048576)
[System.Security.Cryptography.RandomNumberGenerator]::Fill($payload)
$body = [System.Net.Http.ByteArrayContent]::new($payload)
$body.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new('application/octet-stream')
try {
    $response = $client.PostAsync('https://speed.cloudflare.com/__up', $body).GetAwaiter().GetResult()
    "HTTP=$([int]$response.StatusCode)"
    $response.Headers.ToString()
    $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
} finally {
    $body.Dispose()
    $client.Dispose()
}
```

## Provider assessment

### Exit IP: ipify with AWS fallback

- [Official ipify documentation](https://www.ipify.org/) describes IPv4, IPv6 and dual-stack endpoints, JSON, and unlimited requests. This is a provider statement, not an availability guarantee.
- Primary: `https://api64.ipify.org?format=json`. Independent fallback: `https://checkip.amazonaws.com/`, using a different operator and plain-text parser.
- Two ipify hostnames are not independent providers.
- Require HTTP success and a valid public IP; cap response bodies at 4 KiB and reject redirects.
- AWS checkip worked in the live probe. A separate published quota or SLA was not established; use only as fallback with backoff.

### Geolocation: ipapi.is

- [Official documentation](https://ipapi.is/developers.html), updated 2026-10-12, states 1,000 daily lookups for a free keyed account, including commercial use. Each IP in a bulk request consumes one lookup.
- Anonymous access has a 30-lookups/day limit per source IP (IPv6 /64) and a shorter schema. Do not silently use anonymous access as a runtime fallback.
- Keyed fields: `location.country_code`, `location.country`, `location.city`, `asn.asn`, `asn.org`. Missing/null fields are valid.
- Use direct backend POST with `{"q":"<observed IP>","key":"<secret>"}`. The explicitly supplied IP came from a proxied observation; this direct enrichment request is not a proxy availability check.
- Cache for 24 hours by normalized IP, coalesce concurrent lookups, retain stale data with timestamps on refresh failure.
- Respect 429/Retry-After; pause on 403. Do not distribute requests across checked proxy exits to bypass limits.
- The full keyed response and account quota were verified against documentation only: no key was supplied and no account was created.

### Throughput: Cloudflare

- The [official speedtest project](https://github.com/cloudflare/speedtest) documents GET `__down` and POST `__up` and performs sequences of automated measurements.
- The [hosted UI](https://speed.cloudflare.com/) links to [Website and Online Services Terms](https://www.cloudflare.com/website-terms/) effective 2025-08-01. These prohibit overburdening services and circumventing limits, and permit access changes/discontinuation.
- No numeric hosted-endpoint quota or separate explicit guarantee for scheduled backend tests of 100 proxies was found in the reviewed sources. The software license does not grant a hosted-service SLA. Unlimited backend API access is not established.
- Decision: use Cloudflare as a technically verified, replaceable best-effort adapter. Speed scheduling starts disabled. Respect 429/Retry-After and pause the adapter on 403 until manually resumed. Never change proxy exits to evade provider restrictions.
- Do not run the browser-oriented JS measurement engine in the panel: it would measure the browser's route. Implement Go measurements through the selected proxy transport and identify the application's own measurement method.
- Download exposed useful geo headers, but actual names differed partially from Access-Control-Expose-Headers. These are not the primary geolocation contract.

## Verification boundaries

Not yet tested:

- Target Debian/Docker environment or real HTTP/HTTPS/SOCKS5 proxies with/without credentials: none supplied.
- IPv6 egress: the dual-stack IP endpoint returned IPv4 in this run.
- Full keyed ipapi.is schema and actual account quota.
- Real 429/403 behavior: intentionally exhausting limits was unnecessary; exercise these paths with local fixtures.
- Maximum throughput or accuracy of geo coordinates.
- Telegram delivery with a real bot: token and chat ID not supplied.

## Conclusion

Direct HTTPS compatibility from the development machine is confirmed for the selected public endpoints, including exact-size bidirectional transfers. Use ipify + AWS, keyed ipapi.is and a bounded Cloudflare adapter. Real Debian/proxy, keyed geo and Telegram checks remain integration acceptance items, not completed verification.
