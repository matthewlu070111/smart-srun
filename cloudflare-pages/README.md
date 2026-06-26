# SMART SRun Preset Mirror

This directory contains a small Cloudflare Pages site for `srun.edu-publish.site`.

Recommended Pages settings:

- Build command: leave empty
- Build output directory: `cloudflare-pages/public`
- Functions directory: `cloudflare-pages/functions`
- Custom domain: `srun.edu-publish.site`

Runtime endpoints:

- `/` shows a small human-readable status page.
- `/school-presets.json` is the URL used by the OpenWrt plugin.
- `/fallback-school-presets.json` is the static bundled copy used when live GitHub fetch fails.
- `/presets` redirects to `/school-presets.json`.

The Pages Function tries to fetch the upstream raw GitHub JSON from Cloudflare's edge and caches the successful response briefly. If upstream fetch fails or returns an invalid payload, it serves the static fallback copy from Pages assets.

When updating presets, sync both files:

```sh
cp doc/school-presets.json cloudflare-pages/public/fallback-school-presets.json
cp doc/school-presets.json cloudflare-pages/public/school-presets.json
```

