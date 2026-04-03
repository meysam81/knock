# Knock

Bulk-submit sitemap URLs to the Google Indexing API.

## GCP Setup

1. Create a GCP project (or use existing) and enable the **Web Search Indexing API**:

```bash
gcloud services enable indexing.googleapis.com --project=YOUR_PROJECT
```

2. Create a service account and download the key:

```bash
gcloud iam service-accounts create knock\
  --display-name="Knock" \
  --project=YOUR_PROJECT

gcloud iam service-accounts keys create sa-key.json \
  --iam-account=knock@YOUR_PROJECT.iam.gserviceaccount.com
```

3. **Add the service account as an owner in Google Search Console:**
   - Go to [Search Console](https://search.google.com/search-console/) → Settings → Users and permissions
   - Add `knock@YOUR_PROJECT.iam.gserviceaccount.com` as an **Owner**
   - This is the step people miss — without it, every request returns 403

## Install

```bash
go install github.com/meysam81/knock@latest
```

## Usage

```bash
export GOOGLE_APPLICATION_CREDENTIALS=$PWD/sa-key.json

# Dry run — just print discovered URLs
knock --sitemap https://meysam.io/sitemap.xml --dry-run

# Submit all URLs
knock --sitemap https://meysam.io/sitemap.xml --credentials sa-key.json

# With tuning
knock --sitemap https://meysam.io/sitemap.xml \
  --credentials sa-key.json \
  --concurrency 2 \
  --delay-ms 1500
```

## CI Integration (GitHub Actions)

```yaml
- name: Submit URLs
  env:
    GOOGLE_APPLICATION_CREDENTIALS: serviceaccount-key.json
  use: meysam81/knock@main
  with:
    sitemap-url: https://example.com/sitemap.xml
```

## Flags

| Flag            | Default       | Description                       |
| --------------- | ------------- | --------------------------------- |
| `--sitemap`     | (required)    | Sitemap URL                       |
| `--credentials` | ADC fallback  | Path to service account JSON      |
| `--dry-run`     | `false`       | Print URLs without submitting     |
| `--concurrency` | `2`           | Parallel workers                  |
| `--delay-ms`    | `1000`        | Per-worker delay between requests |
| `--type`        | `URL_UPDATED` | `URL_UPDATED` or `URL_DELETED`    |

## Quotas

The Indexing API has a default quota of **200 requests/day**. If you need more,
request a quota increase in the GCP console under "Web Search Indexing API" quotas.

## Caveats

The Indexing API is officially for `JobPosting` and `BroadcastEvent` structured
data. Using it for general pages is an undocumented pattern — it works for many
sites but Google could stop honoring it. This won't get you penalized, but
submissions may be silently ignored.
