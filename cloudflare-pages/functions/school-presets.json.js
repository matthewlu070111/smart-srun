const UPSTREAM_URL =
  "https://raw.githubusercontent.com/matthewlu070111/smart-srun/main/doc/school-presets.json";

const JSON_HEADERS = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
  "Access-Control-Allow-Headers": "content-type",
  "Content-Type": "application/json; charset=utf-8",
  "X-Content-Type-Options": "nosniff"
};

function jsonResponse(text, source, cacheControl) {
  return new Response(text, {
    headers: Object.assign({}, JSON_HEADERS, {
      "Cache-Control": cacheControl,
      "X-Smart-SRun-Preset-Source": source
    })
  });
}

function assertPresetPayload(text) {
  const payload = JSON.parse(text);
  if (!payload || payload.schema_version !== 1 || !Array.isArray(payload.schools)) {
    throw new Error("invalid school-presets payload");
  }
  return text;
}

async function fetchFallback(context) {
  const fallbackUrl = new URL(context.request.url);
  fallbackUrl.pathname = "/fallback-school-presets.json";
  fallbackUrl.search = "";
  const response = await context.env.ASSETS.fetch(fallbackUrl);
  const text = await response.text();
  assertPresetPayload(text);
  return jsonResponse(text, "fallback", "public, max-age=300");
}

export async function onRequestOptions() {
  return new Response(null, {
    status: 204,
    headers: JSON_HEADERS
  });
}

export async function onRequestGet(context) {
  const cache = caches.default;
  const cacheKey = new Request(new URL(context.request.url).origin + "/school-presets.json");
  const cached = await cache.match(cacheKey);
  if (cached) {
    return cached;
  }

  try {
    const upstream = await fetch(UPSTREAM_URL, {
      headers: {
        "Accept": "application/json",
        "User-Agent": "smart-srun-preset-mirror/1"
      },
      cf: {
        cacheTtl: 300,
        cacheEverything: true
      }
    });
    if (!upstream.ok) {
      throw new Error("upstream status " + upstream.status);
    }
    const text = assertPresetPayload(await upstream.text());
    const response = jsonResponse(
      text,
      "github",
      "public, max-age=300, stale-while-revalidate=3600"
    );
    context.waitUntil(cache.put(cacheKey, response.clone()));
    return response;
  } catch (error) {
    return fetchFallback(context);
  }
}

