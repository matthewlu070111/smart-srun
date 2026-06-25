// ==UserScript==
// @name         Smart SRun School Preset Capture
// @namespace    https://github.com/matthewlu070111/smart-srun
// @version      0.1.3
// @description  Capture a successful SRun portal login shape and generate a school-presets.json entry.
// @author       smart-srun maintainers
// @match        http://*/*
// @match        https://*/*
// @run-at       document-start
// @grant        GM_setClipboard
// ==/UserScript==

(function () {
  "use strict";

  var SCRIPT_VERSION = "0.1.3";
  var STORAGE_KEY = "smart_srun_school_preset_capture_v1";
  var STORAGE_TTL_MS = 2 * 60 * 60 * 1000;
  var ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA";
  var DEFAULT_OPERATORS = [
    { id: "cmcc", label: "中国移动" },
    { id: "ctcc", label: "中国电信" },
    { id: "cucc", label: "中国联通" },
    { id: "", label: "校园网" }
  ];

  var state = defaultState();
  var formState = defaultFormState();
  var latestChallenge = "";
  var pendingLoginParams = [];
  var panel = null;
  var panelBody = null;
  var panelHeader = null;
  var statusNode = null;
  var previewNode = null;
  var miniButton = null;
  var dragState = null;
  var detected = false;
  var rawUsername = "";

  function defaultState() {
    return {
      generated_at: "",
      page_origin: "",
      page_pathname: "",
      base_url: "",
      ac_id: "",
      ip: "",
      n: "",
      type: "",
      enc: "",
      double_stack: "",
      login_os: "",
      login_name: "",
      login_seen: false,
      login_success: null,
      login_error: "",
      challenge_seen: false,
      info_seen: false,
      info_prefix: "",
      info_prefix_supported: null,
      info_decoded: false,
      username_masked: "",
      username_has_suffix: null,
      operator_suffix: "",
      inferred_operator: "",
      last_endpoint: "",
      capture_notes: []
    };
  }

  function defaultFormState() {
    return {
      school_id: "",
      school_name: "",
      status: "auto",
      source_issue: "",
      contributors: "",
      ssid: "",
      access_mode: "",
      operator_override: "",
      suffix_override: "",
      ui_left: "",
      ui_top: "",
      ui_minimized: "0",
      ui_hidden: "0",
      ui_step: "0"
    };
  }

  function nowIso() {
    return new Date().toISOString();
  }

  function isSrunUrl(url) {
    var text = String(url || "");
    return (
      text.indexOf("/cgi-bin/get_challenge") !== -1 ||
      text.indexOf("/cgi-bin/srun_portal") !== -1 ||
      text.indexOf("/cgi-bin/rad_user_info") !== -1 ||
      text.indexOf("/cgi-bin/rad_user_dm") !== -1 ||
      text.indexOf("/srun_portal_pc") !== -1
    );
  }

  function looksLikePortalPage() {
    var text = String(location.href || "");
    return (
      text.indexOf("srun") !== -1 ||
      text.indexOf("portal") !== -1 ||
      text.indexOf("ac_id=") !== -1 ||
      text.indexOf("/cgi-bin/") !== -1
    );
  }

  function endpointName(url) {
    var text = String(url || "");
    if (text.indexOf("/cgi-bin/get_challenge") !== -1) {
      return "get_challenge";
    }
    if (text.indexOf("/cgi-bin/srun_portal") !== -1) {
      return "srun_portal";
    }
    if (text.indexOf("/cgi-bin/rad_user_info") !== -1) {
      return "rad_user_info";
    }
    if (text.indexOf("/cgi-bin/rad_user_dm") !== -1) {
      return "rad_user_dm";
    }
    if (text.indexOf("/srun_portal_pc") !== -1) {
      return "portal_page";
    }
    return "unknown";
  }

  function parseUrl(url) {
    var a = document.createElement("a");
    a.href = String(url || "");
    return a;
  }

  function originOf(url) {
    var a = parseUrl(url);
    if (!a.protocol || !a.host) {
      return location.origin;
    }
    return a.protocol + "//" + a.host;
  }

  function pathOnly(url) {
    var a = parseUrl(url);
    return (a.protocol && a.host ? a.protocol + "//" + a.host : location.origin) + a.pathname;
  }

  function safeDecode(value) {
    try {
      return decodeURIComponent(String(value || "").replace(/\+/g, " "));
    } catch (exc) {
      return String(value || "");
    }
  }

  function queryParams(url) {
    var a = parseUrl(url);
    var query = a.search ? a.search.substring(1) : "";
    var out = {};
    var parts;
    var idx;
    var pair;
    var key;
    var value;

    if (!query) {
      return out;
    }

    parts = query.split("&");
    for (idx = 0; idx < parts.length; idx += 1) {
      if (!parts[idx]) {
        continue;
      }
      pair = parts[idx].split("=");
      key = safeDecode(pair.shift() || "");
      value = safeDecode(pair.join("=") || "");
      out[key] = value;
    }
    return out;
  }

  function mergeParams(base, extra) {
    var out = {};
    var key;

    base = base || {};
    extra = extra || {};
    for (key in base) {
      if (hasOwn(base, key)) {
        out[key] = base[key];
      }
    }
    for (key in extra) {
      if (hasOwn(extra, key)) {
        out[key] = extra[key];
      }
    }
    return out;
  }

  function paramsFromText(text) {
    var body = String(text || "");
    var out = {};
    var parts;
    var parsed;
    var key;
    var idx;
    var pair;

    if (!body) {
      return out;
    }
    if (body.charAt(0) === "{") {
      try {
        parsed = JSON.parse(body);
      } catch (exc) {
        parsed = null;
      }
      if (parsed && Object.prototype.toString.call(parsed) === "[object Object]") {
        for (key in parsed) {
          if (hasOwn(parsed, key) && typeof parsed[key] !== "object") {
            out[key] = String(parsed[key]);
          }
        }
        return out;
      }
    }
    if (body.indexOf("=") === -1) {
      return out;
    }

    parts = body.split("&");
    for (idx = 0; idx < parts.length; idx += 1) {
      if (!parts[idx]) {
        continue;
      }
      pair = parts[idx].split("=");
      key = safeDecode(pair.shift() || "");
      out[key] = safeDecode(pair.join("=") || "");
    }
    return out;
  }

  function paramsFromBody(body) {
    var out = {};
    var key;
    var tag;

    if (body === null || typeof body === "undefined") {
      return out;
    }
    if (typeof body === "string") {
      return paramsFromText(body);
    }
    tag = Object.prototype.toString.call(body);
    if (
      (typeof URLSearchParams !== "undefined" && body instanceof URLSearchParams) ||
      tag === "[object URLSearchParams]"
    ) {
      body.forEach(function (value, name) {
        out[name] = String(value);
      });
      return out;
    }
    if (
      (typeof FormData !== "undefined" && body instanceof FormData) ||
      tag === "[object FormData]"
    ) {
      body.forEach(function (value, name) {
        out[name] = typeof value === "string" ? value : "[file]";
      });
      return out;
    }
    if (tag === "[object Object]") {
      for (key in body) {
        if (hasOwn(body, key) && typeof body[key] !== "function" && typeof body[key] !== "object") {
          out[key] = String(body[key]);
        }
      }
    }
    return out;
  }

  function parseJsonpOrJson(text) {
    var body = String(text || "").trim();
    var match;
    if (!body) {
      return null;
    }
    match = body.match(/^[^(]*\(([\s\S]*)\)\s*;?\s*$/);
    if (match) {
      body = match[1];
    }
    try {
      return JSON.parse(body);
    } catch (exc) {
      return null;
    }
  }

  function maskAccount(value) {
    var text = String(value || "").trim();
    var parts;
    var local;
    var suffix;

    if (!text) {
      return "";
    }

    parts = text.split("@");
    local = parts[0];
    suffix = parts.length > 1 ? "@" + parts.slice(1).join("@") : "";

    if (local.length <= 2) {
      return "**" + suffix;
    }
    if (local.length <= 5) {
      return local.charAt(0) + "***" + suffix;
    }
    return local.charAt(0) + "***" + local.slice(-2) + suffix;
  }

  function addNote(note) {
    var text = String(note || "").trim();
    if (!text) {
      return;
    }
    if (state.capture_notes.indexOf(text) === -1) {
      state.capture_notes.push(text);
    }
  }

  function hasOwn(obj, key) {
    return Object.prototype.hasOwnProperty.call(obj || {}, key);
  }

  function captureInfoPrefix(info) {
    var text = String(info || "");
    var match;

    if (!text) {
      return;
    }

    state.info_seen = true;
    match = text.match(/^\{([^}]+)\}/);
    if (match) {
      state.info_prefix = match[1];
      state.info_prefix_supported = match[1] === "SRBX1";
      return;
    }

    state.info_prefix = "";
    state.info_prefix_supported = false;
    addNote("已捕获 info 字段，但未识别到 {SRBX1} 这类前缀。");
  }

  function inferOperatorFromSuffix(suffix) {
    var text = String(suffix || "").toLowerCase();
    if (!text) {
      return "";
    }
    if (
      text.indexOf("cmcc") !== -1 ||
      text.indexOf("mobile") !== -1 ||
      text.indexOf("yidong") !== -1 ||
      text === "yd"
    ) {
      return "cmcc";
    }
    if (
      text.indexOf("ctcc") !== -1 ||
      text.indexOf("telecom") !== -1 ||
      text.indexOf("dianxin") !== -1 ||
      text === "dx"
    ) {
      return "ctcc";
    }
    if (
      text.indexOf("cucc") !== -1 ||
      text.indexOf("unicom") !== -1 ||
      text.indexOf("liantong") !== -1 ||
      text === "lt"
    ) {
      return "cucc";
    }
    if (text === "xn" || text.indexOf("campus") !== -1) {
      return "";
    }
    return "";
  }

  function captureUsername(username, source) {
    var text = String(username || "").trim();
    var parts;
    var suffix;
    var operator;

    if (!text) {
      return;
    }

    rawUsername = text;
    parts = text.split("@");
    state.username_masked = maskAccount(text);
    if (parts.length > 1 && parts.slice(1).join("@").trim()) {
      suffix = parts.slice(1).join("@").trim();
      operator = inferOperatorFromSuffix(suffix);
      state.username_has_suffix = true;
      state.operator_suffix = suffix;
      state.inferred_operator = operator;
      if (!operator) {
        addNote("已捕获后缀 " + suffix + "，但无法自动判断运营商 ID，请人工确认 operator。");
      }
    } else {
      state.username_has_suffix = false;
      state.operator_suffix = "";
      state.inferred_operator = "";
    }

    addNote("username 来源：" + source + "；仅保留后缀和打码账号，不导出账号主体。");
  }

  function updateFromParams(endpoint, url, params, source) {
    var origin = originOf(url);
    var username;

    detected = true;
    state.generated_at = nowIso();
    state.page_origin = location.origin;
    state.page_pathname = location.pathname;
    state.last_endpoint = endpoint;

    if (origin) {
      state.base_url = origin;
    }
    if (params.ac_id) {
      state.ac_id = String(params.ac_id);
    }
    if (params.ip) {
      state.ip = String(params.ip);
    }
    if (params.n) {
      state.n = String(params.n);
    }
    if (params.type) {
      state.type = String(params.type);
    }
    if (params.enc) {
      state.enc = String(params.enc);
    }
    if (hasOwn(params, "double_stack")) {
      state.double_stack = String(params.double_stack);
    }
    if (params.os) {
      state.login_os = String(params.os);
    }
    if (params.name) {
      state.login_name = String(params.name);
    }
    if (params.info) {
      captureInfoPrefix(params.info);
    }

    username = params.username || params.user_name || params.uid || "";
    if (username) {
      captureUsername(username, source || endpoint);
    }

    if (params.callback) {
      installJsonpCallbackWrapper(params.callback, endpoint, url);
    }
    persistState();
    ensurePanelIfRelevant();
    updatePanel();
  }

  function handleRequest(url, transport, method, body) {
    var endpoint;
    var params;

    if (!isSrunUrl(url)) {
      return;
    }

    endpoint = endpointName(url);
    params = mergeParams(queryParams(url), paramsFromBody(body));
    updateFromParams(endpoint, url, params, transport + " " + (method || "GET"));

    if (endpoint === "get_challenge") {
      state.challenge_seen = true;
    }

    if (endpoint === "srun_portal" && String(params.action || "") === "login") {
      state.login_seen = true;
      if (params.info && latestChallenge) {
        decodeAndCaptureInfo(params, latestChallenge);
      } else if (params.info) {
        pendingLoginParams.push(params);
      }
    }
  }

  function handleResponse(endpoint, url, payload, transport) {
    var challenge;
    var idx;

    if (!isSrunUrl(url)) {
      return;
    }

    detected = true;
    if (payload && typeof payload === "object") {
      challenge = payload.challenge || payload.token;
      if (challenge) {
        latestChallenge = String(challenge);
        state.challenge_seen = true;
        for (idx = 0; idx < pendingLoginParams.length; idx += 1) {
          decodeAndCaptureInfo(pendingLoginParams[idx], latestChallenge);
        }
        pendingLoginParams = [];
      }

      if (endpoint === "srun_portal") {
        captureLoginResult(payload);
      }
    }

    addNote("捕获响应：" + endpoint + " via " + transport + " at " + pathOnly(url));
    persistState();
    ensurePanelIfRelevant();
    updatePanel();
  }

  function captureLoginResult(payload) {
    var error = String(payload.error || "");
    var res = String(payload.res || "");
    var errorMsg = String(payload.error_msg || payload.suc_msg || "");
    var lower = (error + " " + res + " " + errorMsg).toLowerCase();

    if (lower.indexOf("ok") !== -1 || lower.indexOf("success") !== -1) {
      state.login_success = true;
    } else if (errorMsg || error || res) {
      state.login_success = false;
    }
    state.login_error = errorMsg || error || res || "";
  }

  function decodeAndCaptureInfo(params, token) {
    var info = String(params.info || "");
    var encoded;
    var binary;
    var jsonText;
    var decoded;

    captureInfoPrefix(info);
    if (state.info_prefix !== "SRBX1") {
      if (state.info_prefix) {
        addNote("info 前缀为 {" + state.info_prefix + "}，当前脚本只支持解码 {SRBX1}；已记录前缀但未解码。");
      }
      return;
    }

    try {
      encoded = info.substring(state.info_prefix.length + 2);
      binary = srunBase64Decode(encoded, ALPHA);
      jsonText = xxteaDecrypt(binary, token);
      decoded = JSON.parse(jsonText);
    } catch (exc) {
      addNote("info 解码失败：" + String(exc && exc.message ? exc.message : exc));
      return;
    }

    state.info_decoded = true;
    if (decoded && decoded.username) {
      captureUsername(decoded.username, "decoded info.username");
    }
    if (decoded && decoded.acid && !state.ac_id) {
      state.ac_id = String(decoded.acid);
    }
    if (decoded && decoded.ip && !state.ip) {
      state.ip = String(decoded.ip);
    }
    if (decoded && decoded.enc_ver && !state.enc) {
      state.enc = String(decoded.enc_ver);
    }
  }

  function buildPresetEntry() {
    var suffix = String(readField("suffix_override") || state.operator_suffix || "").trim();
    var operator = String(readField("operator_override") || "").trim().toLowerCase();
    var preferredOperator;
    var hasPreferredOperator;
    var status = readField("status");
    var defaults = {};
    var entry;
    var contributors;
    var description;
    var manualDescription;
    var operators;
    var observed;

    if (!operator && suffix) {
      operator = suffix.toLowerCase();
    }
    hasPreferredOperator = !!operator || !!suffix || state.username_has_suffix === false;
    preferredOperator = operator || (state.username_has_suffix === false ? "" : "");
    if (status === "auto") {
      status = state.login_success === true && state.base_url && state.ac_id ? "active" : "draft";
    }

    if (state.base_url) {
      defaults.base_url = state.base_url;
    }
    if (state.ac_id) {
      defaults.ac_id = state.ac_id;
    }
    if (readField("ssid")) {
      defaults.ssid = readField("ssid");
    }
    if (readField("access_mode")) {
      defaults.access_mode = readField("access_mode");
    }
    manualDescription = readField("description");
    if (manualDescription) {
      description = manualDescription;
    } else {
      description = "";
    }

    contributors = splitList(readField("contributors"));
    operators = buildOperatorsForEntry(preferredOperator, hasPreferredOperator);
    observed = buildObservedLoginShape();
    entry = {
      id: sanitizeSchoolId(readField("school_id")),
      name: readField("school_name") || "",
      status: status || "draft",
      description: description,
      defaults: defaults,
      contributors: contributors,
      source_issue: readField("source_issue") || ""
    };
    if (operators.length) {
      entry.operators = operators;
    }
    if (objectHasKeys(observed)) {
      entry.observed_login_shape = observed;
    }

    return entry;
  }

  function objectHasKeys(obj) {
    var key;
    for (key in obj) {
      if (hasOwn(obj, key)) {
        return true;
      }
    }
    return false;
  }

  function buildObservedLoginShape() {
    var observed = {};
    var count = 0;

    function put(key, value) {
      var text;
      if (value === null || value === undefined) {
        return;
      }
      text = String(value || "").trim();
      if (!text) {
        return;
      }
      observed[key] = text;
      count += 1;
    }

    put("n", state.n);
    put("type", state.type);
    put("enc", state.enc);
    put("double_stack", state.double_stack);
    put("os", state.login_os);
    put("name", state.login_name);
    put("info_prefix", state.info_prefix);

    return count ? observed : {};
  }

  function pushUniqueOperator(operators, operatorId, label) {
    var op = String(operatorId || "").trim().toLowerCase();
    var idx;
    for (idx = 0; idx < operators.length; idx += 1) {
      if (operators[idx].id === op) {
        return;
      }
    }
    operators.push({ id: op, label: label || operatorLabelFromSuffix(op) });
  }

  function buildOperatorsForEntry(operator, hasPreferredOperator) {
    var op = String(operator || "").trim().toLowerCase();
    var operators = [];
    var idx;

    if (hasPreferredOperator) {
      pushUniqueOperator(operators, op, operatorLabelFromSuffix(op));
    }
    for (idx = 0; idx < DEFAULT_OPERATORS.length; idx += 1) {
      pushUniqueOperator(
        operators,
        DEFAULT_OPERATORS[idx].id,
        DEFAULT_OPERATORS[idx].label
      );
    }
    return operators;
  }

  function operatorLabelFromSuffix(suffix) {
    var inferred = inferOperatorFromSuffix(suffix);
    var idx;
    for (idx = 0; idx < DEFAULT_OPERATORS.length; idx += 1) {
      if (DEFAULT_OPERATORS[idx].id === inferred) {
        return DEFAULT_OPERATORS[idx].label;
      }
    }
    return suffix;
  }

  function buildCaptureSummary() {
    return {
      tool: "smart-srun school preset capture",
      version: SCRIPT_VERSION,
      generated_at: nowIso(),
      redaction: "raw username/password/challenge/info are not exported; username suffix and masked username may be included.",
      page: {
        origin: location.origin,
        pathname: location.pathname,
        userAgent: navigator.userAgent
      },
      observed: state,
      preset_entry: buildPresetEntry()
    };
  }

  function splitList(value) {
    var parts = String(value || "").split(",");
    var out = [];
    var idx;
    var text;
    for (idx = 0; idx < parts.length; idx += 1) {
      text = parts[idx].trim();
      if (text) {
        out.push(text);
      }
    }
    return out;
  }

  function sanitizeSchoolId(value) {
    var text = String(value || "").trim().toLowerCase();
    text = text.replace(/[^a-z0-9_.-]+/g, "-").replace(/^-+|-+$/g, "");
    return text;
  }

  function readField(key) {
    var node = document.getElementById("ssp-" + key);
    if (node) {
      formState[key] = String(node.value || "");
      return formState[key];
    }
    return formState[key] || "";
  }

  function persistState() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({
        saved_at_ms: Date.now(),
        origin: location.origin,
        state: state,
        form: formState
      }));
    } catch (exc) {
      // Ignore disabled storage.
    }
  }

  function restoreState() {
    var raw;
    var saved;
    var ageMs;

    try {
      raw = localStorage.getItem(STORAGE_KEY);
    } catch (exc) {
      raw = "";
    }
    if (!raw) {
      return;
    }
    try {
      saved = JSON.parse(raw);
    } catch (exc2) {
      return;
    }
    if (!saved || saved.origin !== location.origin) {
      return;
    }
    ageMs = Date.now() - Number(saved.saved_at_ms || 0);
    if (ageMs < 0 || ageMs > STORAGE_TTL_MS) {
      clearState();
      return;
    }
    if (saved.state && typeof saved.state === "object") {
      state = mergeState(defaultState(), saved.state);
      detected = !!state.login_seen || !!state.challenge_seen || !!state.base_url;
    }
    if (saved.form && typeof saved.form === "object") {
      formState = mergeState(defaultFormState(), saved.form);
    }
  }

  function mergeState(base, extra) {
    var out = {};
    var key;
    for (key in base) {
      if (Object.prototype.hasOwnProperty.call(base, key)) {
        out[key] = base[key];
      }
    }
    for (key in extra) {
      if (Object.prototype.hasOwnProperty.call(extra, key)) {
        out[key] = extra[key];
      }
    }
    if (
      Object.prototype.hasOwnProperty.call(base, "capture_notes") &&
      (!out.capture_notes || Object.prototype.toString.call(out.capture_notes) !== "[object Array]")
    ) {
      out.capture_notes = [];
    }
    return out;
  }

  function clearState() {
    var uiState = {
      ui_left: formState.ui_left || "",
      ui_top: formState.ui_top || "",
      ui_minimized: formState.ui_minimized || "0",
      ui_hidden: formState.ui_hidden || "0"
    };
    state = defaultState();
    formState = defaultFormState();
    formState.ui_left = uiState.ui_left;
    formState.ui_top = uiState.ui_top;
    formState.ui_minimized = uiState.ui_minimized;
    formState.ui_hidden = uiState.ui_hidden;
    latestChallenge = "";
    pendingLoginParams = [];
    detected = false;
    try {
      localStorage.removeItem(STORAGE_KEY);
    } catch (exc) {
      // Ignore disabled storage.
    }
  }

  function ensurePanelIfRelevant() {
    if (panel || !document.body) {
      return;
    }
    if (!detected && !looksLikePortalPage()) {
      return;
    }
    if (formState.ui_hidden === "1") {
      showMiniButton();
      return;
    }
    ensurePanel();
  }

  function ensurePanel() {
    var title;
    var controls;
    var minimizeButton;
    var hideButton;

    if (panel || !document.body) {
      return;
    }



    panel = document.createElement("div");
    panel.style.cssText = panelStyle();
    applyPanelPosition();

    panelHeader = document.createElement("div");
    panelHeader.style.cssText = [
      "display:flex",
      "align-items:center",
      "justify-content:space-between",
      "gap:8px",
      "padding:8px 10px",
      "background:#181825",
      "border-bottom:1px solid #313244",
      "cursor:move",
      "user-select:none"
    ].join(";");

    title = document.createElement("div");
    title.style.cssText = "font-weight:700;line-height:1.2;font-size:12px;color:#cdd6f4;";
    title.textContent = "Smart SRun 捕获运营商后缀";
    panelHeader.appendChild(title);

    controls = document.createElement("div");
    controls.style.cssText = "display:flex;gap:5px;flex:0 0 auto;";
    minimizeButton = makeHeaderButton(formState.ui_minimized === "1" ? "展开" : "收起");
    minimizeButton.onclick = toggleMinimized;
    hideButton = makeHeaderButton("隐藏");
    hideButton.onclick = hidePanel;
    controls.appendChild(minimizeButton);
    controls.appendChild(hideButton);
    panelHeader.appendChild(controls);
    panel.appendChild(panelHeader);

    panelBody = document.createElement("div");
    panelBody.style.cssText = "padding:0;";
    panel.appendChild(panelBody);

    document.body.appendChild(panel);
    hideMiniButton();
    makeDraggable(panelHeader);
    clampPanelToViewport();
    renderStep();
    applyMinimized();
    updatePanel();
  }

  function panelStyle() {
    return [
      "position:fixed",
      "z-index:2147483647",
      "background:#1e1e2e",
      "color:#cdd6f4",
      "font:13px/1.4 -apple-system,BlinkMacSystemFont,Segoe UI,sans-serif",
      "border:1px solid #313244",
      "border-radius:12px",
      "box-shadow:0 10px 30px rgba(0,0,0,.4)",
      "overflow:hidden",
      "width:340px",
      "max-width:calc(100vw - 24px)"
    ].join(";");
  }

  function applyPanelPosition() {
    if (!panel) {
      return;
    }
    if (formState.ui_left || formState.ui_top) {
      panel.style.left = (formState.ui_left || "12") + "px";
      panel.style.top = (formState.ui_top || "12") + "px";
      panel.style.right = "auto";
      panel.style.bottom = "auto";
    } else {
      panel.style.right = "12px";
      panel.style.bottom = "12px";
      panel.style.left = "auto";
      panel.style.top = "auto";
    }
  }

  function makeDraggable(handle) {
    if (!handle) {
      return;
    }
    handle.onmousedown = function (event) {
      var rect;
      if (isInteractiveTarget(event.target)) {
        return;
      }
      if (!panel) {
        return;
      }
      rect = panel.getBoundingClientRect();
      dragState = {
        startX: event.clientX,
        startY: event.clientY,
        left: rect.left,
        top: rect.top
      };
      document.addEventListener("mousemove", onDragMove, true);
      document.addEventListener("mouseup", stopDrag, true);
      event.preventDefault();
    };
  }

  function isInteractiveTarget(target) {
    var tag = target && target.tagName ? String(target.tagName).toLowerCase() : "";
    return tag === "button" || tag === "input" || tag === "select" || tag === "textarea" || tag === "a";
  }

  function onDragMove(event) {
    if (!dragState || !panel) {
      return;
    }
    movePanel(
      dragState.left + event.clientX - dragState.startX,
      dragState.top + event.clientY - dragState.startY,
      false
    );
    event.preventDefault();
  }

  function stopDrag() {
    if (dragState && panel) {
      formState.ui_left = String(Math.round(panel.getBoundingClientRect().left));
      formState.ui_top = String(Math.round(panel.getBoundingClientRect().top));
      persistState();
    }
    dragState = null;
    document.removeEventListener("mousemove", onDragMove, true);
    document.removeEventListener("mouseup", stopDrag, true);
  }

  function movePanel(left, top, shouldPersist) {
    var next = clampPosition(left, top);
    if (!panel) {
      return;
    }
    panel.style.left = next.left + "px";
    panel.style.top = next.top + "px";
    panel.style.right = "auto";
    panel.style.bottom = "auto";
    if (shouldPersist) {
      formState.ui_left = String(Math.round(next.left));
      formState.ui_top = String(Math.round(next.top));
      persistState();
    }
  }

  function clampPosition(left, top) {
    var margin = 8;
    var width = panel ? (panel.offsetWidth || 460) : 460;
    var height = panel ? (panel.offsetHeight || 260) : 260;
    var maxLeft = Math.max(margin, window.innerWidth - width - margin);
    var maxTop = Math.max(margin, window.innerHeight - height - margin);
    return {
      left: Math.max(margin, Math.min(Number(left) || margin, maxLeft)),
      top: Math.max(margin, Math.min(Number(top) || margin, maxTop))
    };
  }

  function clampPanelToViewport() {
    var rect;
    if (!panel) {
      return;
    }
    rect = panel.getBoundingClientRect();
    movePanel(rect.left, rect.top, false);
  }

  function toggleMinimized() {
    formState.ui_minimized = formState.ui_minimized === "1" ? "0" : "1";
    applyMinimized();
    persistState();
  }

  function applyMinimized() {
    var buttons;
    if (panelBody) {
      panelBody.style.display = formState.ui_minimized === "1" ? "none" : "block";
    }
    if (panelHeader) {
      buttons = panelHeader.getElementsByTagName("button");
      if (buttons && buttons.length > 0) {
        buttons[0].textContent = formState.ui_minimized === "1" ? "展开" : "收起";
      }
    }
  }

  function hidePanel() {
    formState.ui_hidden = "1";
    persistState();
    if (panel && panel.parentNode) {
      panel.parentNode.removeChild(panel);
    }
    panel = null;
    panelBody = null;
    panelHeader = null;
    statusNode = null;
    previewNode = null;
    showMiniButton();
  }

  function showPanel() {
    formState.ui_hidden = "0";
    persistState();
    hideMiniButton();
    ensurePanel();
  }

  function showMiniButton() {
    if (!document.body) {
      return;
    }
    if (!miniButton) {
      miniButton = document.createElement("button");
      miniButton.type = "button";
      miniButton.onclick = showPanel;
      miniButton.style.cssText = [
        "position:fixed",
        "right:12px",
        "bottom:12px",
        "z-index:2147483647",
        "padding:8px 10px",
        "border:1px solid #374151",
        "border-radius:999px",
        "background:#111827",
        "color:#f9fafb",
        "font:12px/1.2 -apple-system,BlinkMacSystemFont,Segoe UI,sans-serif",
        "box-shadow:0 6px 18px rgba(0,0,0,.25)",
        "cursor:pointer"
      ].join(";");
      document.body.appendChild(miniButton);
    }
    miniButton.textContent = miniButtonText();
  }

  function hideMiniButton() {
    if (miniButton && miniButton.parentNode) {
      miniButton.parentNode.removeChild(miniButton);
    }
    miniButton = null;
  }

  function miniButtonText() {
    if (state.login_success === true) {
      return "SRun 采集完成";
    }
    if (state.login_seen) {
      return "SRun 已捕获登录";
    }
    return "SRun 预设采集";
  }

  function loginShapeLabel() {
    var parts = [];
    if (state.info_prefix) {
      parts.push("info={" + state.info_prefix + "}");
    }
    if (state.enc) {
      parts.push("enc=" + state.enc);
    }
    if (state.n) {
      parts.push("n=" + state.n);
    }
    if (state.type) {
      parts.push("type=" + state.type);
    }
    if (state.double_stack) {
      parts.push("double_stack=" + state.double_stack);
    }
    if (state.login_os) {
      parts.push("os=" + state.login_os);
    }
    if (state.login_name) {
      parts.push("name=" + state.login_name);
    }
    if (state.info_seen && !state.info_decoded) {
      parts.push("info未解码");
    }
    return parts.length ? parts.join(", ") : "未捕获";
  }

  var lastRenderedCaptured = null;

  function renderStep() {
    if (!panelBody) {
      return;
    }
    injectStyles();
    var suffix = readField("suffix_override") || state.operator_suffix || "";
    var isCaptured = state.login_seen || !!suffix || !!state.base_url;
    lastRenderedCaptured = panelRenderKey(isCaptured);

    previewNode = null;
    panelBody.innerHTML = panelContentHtml();
    previewNode = panelBody.querySelector("textarea");
    wirePanelButtons();
    updatePanel();
  }

  function injectStyles() {
    if (document.getElementById("ssp-styles")) {
      return;
    }
    var style = document.createElement("style");
    style.id = "ssp-styles";
    style.textContent =
      "@keyframes ssp-pulse { 0% { opacity: 0.4; } 50% { opacity: 1; } 100% { opacity: 0.4; } }\n" +
      ".ssp-copyable:hover { color: #89b4fa !important; border-bottom-color: #89b4fa !important; }";
    document.head.appendChild(style);
  }

  function panelContentHtml() {
    var suffix = readField("suffix_override") || state.operator_suffix || "";
    var isCaptured = state.login_seen || !!suffix || !!state.base_url;

    if (!isCaptured) {
      return '<div style="padding: 20px 16px; color: #cdd6f4; text-align: center;">' +
        '<div style="font-size: 32px; margin-bottom: 12px; animation: ssp-pulse 1.5s infinite ease-in-out;">⏳</div>' +
        '<div style="font-size: 14px; font-weight: bold; color: #f9e2af; margin-bottom: 8px;">等待登录中...</div>' +
        '<div style="font-size: 12px; color: #a6adc8; line-height: 1.5; margin-bottom: 16px;">' +
        '请在网页登录框中，输入您的账号和密码并进行登录。<br/>登录成功后，此处会自动生成配置数据。' +
        '</div>' +
        '<div style="border-top: 1px solid #313244; padding-top: 12px; display: flex; gap: 8px; justify-content: center;">' +
        '<button type="button" id="ssp-refresh" style="' + buttonStyle(false) + '">手动刷新</button>' +
        '</div>' +
        '</div>';
    }

    var accountVal = rawUsername ? rawUsername.split("@")[0] : (state.username_masked ? state.username_masked.split("@")[0] : "未捕获");
    var operatorLabel = suffix ? operatorLabelFromSuffix(suffix) : "校园网";
    var suffixVal = suffix ? suffix : "无后缀（留空）";
    var baseUrlVal = state.base_url || "未捕获";
    var acIdVal = state.ac_id || "自动";
    var loginShapeVal = loginShapeLabel();
    var resultTitle;
    var resultText;
    var resultColor;

    if (state.login_success === true && formState.ui_step === "submit") {
      return '<div style="padding: 12px; color: #cdd6f4;">' +
        '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:10px;">' +
        '<span style="font-size:13px;font-weight:bold;color:#a6e3a1;">提交信息，协助开发者</span>' +
        '<button type="button" id="ssp-back-summary" style="background:none;border:none;color:#89b4fa;cursor:pointer;font-size:11px;padding:0;outline:none;">返回</button>' +
        '</div>' +
        rowHtml("学校 ID", inputHtml("school_id", "example-university")) +
        rowHtml("学校名称", inputHtml("school_name", "某某大学")) +
        rowHtml("状态", selectHtml("status", [["active", "active"], ["draft", "draft"], ["deprecated", "deprecated"]])) +
        rowHtml("描述", inputHtml("description", "")) +
        '<textarea readonly style="' + textareaStyle() + '"></textarea>' +
        '<div style="display:flex;gap:6px;margin-top:6px;">' +
        '<button type="button" id="ssp-copy-entry" style="' + buttonStyle(false) + ' flex:1;">复制 JSON</button>' +
        '<button type="button" id="ssp-download" style="' + buttonStyle(false) + ' flex:1;">下载 JSON</button>' +
        '</div>' +
        '<div style="margin-top:10px;padding:8px;border:1px solid #313244;border-radius:6px;background:#181825;color:#a6adc8;font-size:11px;line-height:1.5;">' +
        '请确认学校 ID、学校名称、状态和描述后，再把 JSON 粘贴到 PR 或 Issue。PR 适合直接提交预设；不熟悉 GitHub 流程时可以先发 Issue。' +
        '</div>' +
        '<div style="display:flex;gap:6px;margin-top:8px;">' +
        '<a href="https://github.com/matthewlu070111/smart-srun/pulls" target="_blank" style="' + linkButtonStyle(true) + '">提交 PR</a>' +
        '<a href="https://github.com/matthewlu070111/smart-srun/issues/new/choose" target="_blank" style="' + linkButtonStyle(false) + '">提交 Issue</a>' +
        '</div>' +
        '</div>';
    }

    if (state.login_success === true) {
      resultTitle = "登录成功，已获取配置数据";
      resultText = "下方信息来自这次真实网页登录请求。确认无误后，可以继续提交信息协助开发者维护学校预设。";
      resultColor = "#a6e3a1";
    } else if (state.login_success === false) {
      resultTitle = "登录失败，但已捕获请求信息";
      resultText = "网页登录返回失败：" + (state.login_error || "未知原因") + "。请先检查账号密码或网络状态后重试；当前已捕获字段仍可复制给维护者排查。";
      resultColor = "#f38ba8";
    } else {
      resultTitle = "已捕获登录请求，等待登录结果";
      resultText = "脚本已经看到登录请求，但还没有确认网页登录成功。请等待页面返回，或重新尝试登录。";
      resultColor = "#f9e2af";
    }

    return '<div style="padding: 12px; color: #cdd6f4;">' +
      '<div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px;">' +
      '<span style="font-size: 13px; font-weight: bold; color: ' + resultColor + ';">' + escapeHtml(resultTitle) + '</span>' +
      '<button type="button" id="ssp-clear" style="background: none; border: none; color: #f38ba8; cursor: pointer; font-size: 11px; padding: 0; outline: none;">清空数据</button>' +
      '</div>' +
      '<div style="font-size: 11px; color: #a6adc8; margin-bottom: 8px; line-height: 1.5;">' +
      escapeHtml(resultText) + '<br/>点击下方带有下划线的值即可复制。' +
      '</div>' +
      '<div style="display: grid; grid-template-columns: 85px 1fr; gap: 8px 12px; background: #181825; padding: 12px; border-radius: 8px; border: 1px solid #313244; line-height: 1.5; font-size: 12px;">' +
      '<div style="color: #89b4fa; font-weight: bold;">学工号</div>' +
      '<div class="ssp-copyable" data-val="' + escapeHtml(accountVal) + '" style="color: #cdd6f4; font-family: monospace; cursor: pointer; border-bottom: 1px dotted #a6adc8; word-break: break-all; display: inline-block;" title="点击复制">' + escapeHtml(accountVal) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">运营商</div>' +
      '<div class="ssp-copyable" data-val="' + escapeHtml(operatorLabel) + '" style="color: #cdd6f4; cursor: pointer; border-bottom: 1px dotted #a6adc8; display: inline-block;" title="点击复制">' + escapeHtml(operatorLabel) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">运营商后缀</div>' +
      '<div class="ssp-copyable" data-val="' + (suffix ? escapeHtml(suffix) : "") + '" style="color: #cdd6f4; font-family: monospace; cursor: pointer; border-bottom: 1px dotted #a6adc8; display: inline-block;" title="点击复制">' + escapeHtml(suffixVal) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">密码</div>' +
      '<div style="color: #9399b2; font-style: italic;">•••••• (您的校园网登录密码)</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">认证地址</div>' +
      '<div class="ssp-copyable" data-val="' + escapeHtml(baseUrlVal) + '" style="color: #cdd6f4; font-family: monospace; cursor: pointer; border-bottom: 1px dotted #a6adc8; word-break: break-all; display: inline-block;" title="点击复制">' + escapeHtml(baseUrlVal) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">AC_ID</div>' +
      '<div class="ssp-copyable" data-val="' + escapeHtml(acIdVal) + '" style="color: #cdd6f4; font-family: monospace; cursor: pointer; border-bottom: 1px dotted #a6adc8; display: inline-block;" title="点击复制">' + escapeHtml(acIdVal) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">登录形态</div>' +
      '<div class="ssp-copyable" data-val="' + (loginShapeVal === "未捕获" ? "" : escapeHtml(loginShapeVal)) + '" style="color: #cdd6f4; font-family: monospace; cursor: pointer; border-bottom: 1px dotted #a6adc8; word-break: break-all; display: inline-block;" title="点击复制">' + escapeHtml(loginShapeVal) + '</div>' +
      '<div style="color: #89b4fa; font-weight: bold;">校园网 SSID</div>' +
      '<div style="color: #9399b2; font-style: italic;">(您当前连接的 Wi-Fi 名称)</div>' +
      '</div>' +
      (state.login_success === true ?
        '<button type="button" id="ssp-submit-info" style="' + buttonStyle(true) + ' width:100%; margin-top:12px;">提交信息，协助开发者</button>' :
        '<div style="display:flex;gap:6px;margin-top:12px;"><button type="button" id="ssp-copy-summary" style="' + buttonStyle(false) + ' flex:1;">复制诊断信息</button><button type="button" id="ssp-refresh" style="' + buttonStyle(false) + ' flex:1;">重新检查</button></div>'
      ) +
      '<details style="margin-top: 12px; font-size: 11px; border-top: 1px solid #313244; padding-top: 8px;">' +
      '<summary style="cursor: pointer; color: #a6adc8; outline: none; user-select: none;">高级：查看生成的预设 JSON</summary>' +
      '<textarea readonly style="' + textareaStyle() + '"></textarea>' +
      '<div style="display: flex; gap: 6px; margin-top: 6px;">' +
      '<button type="button" id="ssp-copy-entry" style="' + buttonStyle(false) + ' width: 50%; margin: 0;">复制预设 JSON</button>' +
      '<button type="button" id="ssp-download" style="' + buttonStyle(false) + ' width: 50%; margin: 0;">下载文件</button>' +
      '</div>' +
      '</details>' +
      '</div>';
  }

  function wirePanelButtons() {
    var refresh = document.getElementById("ssp-refresh");
    var clear = document.getElementById("ssp-clear");
    var copyEntry = document.getElementById("ssp-copy-entry");
    var download = document.getElementById("ssp-download");
    var copySummary = document.getElementById("ssp-copy-summary");
    var submitInfo = document.getElementById("ssp-submit-info");
    var backSummary = document.getElementById("ssp-back-summary");
    var copyables = panelBody ? panelBody.querySelectorAll(".ssp-copyable") : [];
    var formInputs = panelBody ? panelBody.querySelectorAll("input,select") : [];

    if (refresh) {
      refresh.onclick = function () {
        var button = refresh;
        updatePanel();
        button = document.getElementById("ssp-refresh") || button;
        setButtonFeedback(button, "已重新检查");
      };
    }
    if (clear) {
      clear.onclick = function () {
        clearState();
        rawUsername = "";
        renderStep();
      };
    }
    if (copyEntry) {
      copyEntry.onclick = function () {
        copyPresetEntry(copyEntry);
      };
    }
    if (download) {
      download.onclick = function () {
        downloadPresetEntry();
        setButtonFeedback(download, "已下载");
      };
    }
    if (copySummary) {
      copySummary.onclick = function () {
        copyCaptureSummary(copySummary);
      };
    }
    if (submitInfo) {
      submitInfo.onclick = function () {
        formState.ui_step = "submit";
        if (!formState.status || formState.status === "auto") {
          formState.status = "active";
        }
        persistState();
        renderStep();
      };
    }
    if (backSummary) {
      backSummary.onclick = function () {
        formState.ui_step = "0";
        persistState();
        renderStep();
      };
    }
    
    var idx;
    for (idx = 0; idx < copyables.length; idx += 1) {
      (function (el) {
        el.onclick = function () {
          var val = el.getAttribute("data-val");
          copyField(el, val);
        };
      })(copyables[idx]);
    }
    for (idx = 0; idx < formInputs.length; idx += 1) {
      formInputs[idx].oninput = function () {
        updatePanel();
        persistState();
      };
      formInputs[idx].onchange = formInputs[idx].oninput;
    }
  }

  function setButtonFeedback(button, message) {
    var oldText;
    var oldColor;
    if (!button) {
      return;
    }
    oldText = button.textContent;
    oldColor = button.style.color;
    button.textContent = message;
    button.style.color = "#a6e3a1";
    setTimeout(function () {
      button.textContent = oldText;
      button.style.color = oldColor;
    }, 1100);
  }

  function copyField(el, val) {
    if (!val) {
      return;
    }
    copyText(val, function (ok, message) {
      var oldText = el.textContent;
      var oldStyle = el.style.color;
      el.style.color = ok ? "#a6e3a1" : "#f9e2af";
      el.textContent = message || (ok ? "已复制" : "请手动复制");
      setTimeout(function () {
        el.style.color = oldStyle;
        el.textContent = oldText;
      }, 900);
    });
  }

  function rowHtml(label, controlHtml) {
    return '<div style="display:flex;gap:8px;align-items:center;margin:5px 0;">' +
      '<label style="width:72px;color:#cbd5e1;">' + escapeHtml(label) + '</label>' +
      '<div style="flex:1;">' + controlHtml + '</div>' +
      '</div>';
  }

  function inputHtml(key, placeholder) {
    return '<input id="ssp-' + key + '" value="' + escapeHtml(formState[key] || "") +
      '" placeholder="' + escapeHtml(placeholder || "") + '" style="' + inputStyle() + '">';
  }

  function selectHtml(key, options) {
    var html = '<select id="ssp-' + key + '" style="' + inputStyle() + '">';
    var idx;
    var selected;
    for (idx = 0; idx < options.length; idx += 1) {
      selected = String(formState[key] || "") === String(options[idx][0]) ? " selected" : "";
      html += '<option value="' + escapeHtml(options[idx][0]) + '"' + selected + '>' +
        escapeHtml(options[idx][1]) + '</option>';
    }
    html += "</select>";
    return html;
  }

  function inputStyle() {
    return [
      "box-sizing:border-box",
      "width:100%",
      "padding:4px 6px",
      "border:1px solid #313244",
      "border-radius:4px",
      "background:#1f2937",
      "color:#f9fafb"
    ].join(";");
  }

  function textareaStyle() {
    return [
      "box-sizing:border-box",
      "width:100%",
      "height:120px",
      "margin:4px 0",
      "padding:7px",
      "border:1px solid #313244",
      "border-radius:6px",
      "background:#11111b",
      "color:#a6e3a1",
      "font:11px/1.35 ui-monospace,SFMono-Regular,Consolas,monospace",
      "resize:vertical"
    ].join(";");
  }

  function updatePanel() {
    var suffix = readField("suffix_override") || state.operator_suffix || "";
    var isCaptured = state.login_seen || !!suffix || !!state.base_url;
    
    if (lastRenderedCaptured !== panelRenderKey(isCaptured)) {
      renderStep();
      return;
    }
    
    if (previewNode) {
      previewNode.value = JSON.stringify(buildPresetEntry(), null, 2);
    }
    if (miniButton) {
      miniButton.textContent = miniButtonText();
    }
  }

  function panelRenderKey(isCaptured) {
    return [
      isCaptured ? "1" : "0",
      String(state.login_success),
      String(formState.ui_step || "0")
    ].join("|");
  }

  function makeHeaderButton(text) {
    var button = document.createElement("button");
    button.type = "button";
    button.textContent = text;
    button.style.cssText = [
      "padding:3px 8px",
      "border:1px solid #45475a",
      "border-radius:4px",
      "background:#313244",
      "color:#cdd6f4",
      "font:11px -apple-system,BlinkMacSystemFont,Segoe UI,sans-serif",
      "cursor:pointer",
      "outline:none"
    ].join(";");
    return button;
  }

  function buttonStyle(primary) {
    return [
      "padding:6px 12px",
      "border:1px solid #45475a",
      "border-radius:6px",
      "background:" + (primary ? "#89b4fa" : "#313244"),
      "color:" + (primary ? "#11111b" : "#cdd6f4"),
      "font-weight:" + (primary ? "bold" : "normal"),
      "cursor:pointer",
      "font-size:12px",
      "transition: background 0.2s"
    ].join(";");
  }

  function linkButtonStyle(primary) {
    return [
      buttonStyle(primary),
      "display:block",
      "flex:1",
      "text-align:center",
      "text-decoration:none",
      "box-sizing:border-box"
    ].join(";");
  }

  function copyPresetEntry(button) {
    copyText(JSON.stringify(buildPresetEntry(), null, 2), function (ok, message) {
      setButtonFeedback(button, message || (ok ? "已复制" : "请手动复制"));
    });
  }

  function copyCaptureSummary(button) {
    copyText(JSON.stringify(buildCaptureSummary(), null, 2), function (ok, message) {
      setButtonFeedback(button, message || (ok ? "已复制" : "请手动复制"));
    });
  }

  function copyText(text, done) {
    try {
      if (typeof GM_setClipboard === "function") {
        GM_setClipboard(text, "text");
        if (done) {
          done(true, "已复制");
        }
        return;
      }
    } catch (exc) {
      // Fall through to the browser clipboard API.
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function () {
        if (done) {
          done(true, "已复制");
        }
      }, function () {
        window.prompt("复制下面的内容", text);
        if (done) {
          done(false, "请手动复制");
        }
      });
    } else {
      window.prompt("复制下面的内容", text);
      if (done) {
        done(false, "请手动复制");
      }
    }
  }

  function downloadPresetEntry() {
    var blob = new Blob([JSON.stringify(buildPresetEntry(), null, 2)], {
      type: "application/json;charset=utf-8"
    });
    var a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "smart-srun-school-preset-entry-" + Date.now() + ".json";
    document.body.appendChild(a);
    a.click();
    setTimeout(function () {
      URL.revokeObjectURL(a.href);
      if (a.parentNode) {
        a.parentNode.removeChild(a);
      }
    }, 1000);
  }

  function escapeHtml(value) {
    return String(value || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function installJsonpCallbackWrapper(callbackName, endpoint, url) {
    var current;
    if (!callbackName || typeof callbackName !== "string") {
      return;
    }
    if (window[callbackName] && window[callbackName].__srunPresetCaptureWrapped) {
      return;
    }

    current = window[callbackName];
    if (typeof current === "function") {
      window[callbackName] = wrapJsonpCallback(current, endpoint, url);
      return;
    }

    try {
      Object.defineProperty(window, callbackName, {
        configurable: true,
        enumerable: true,
        get: function () {
          return current;
        },
        set: function (fn) {
          current = typeof fn === "function" ? wrapJsonpCallback(fn, endpoint, url) : fn;
        }
      });
    } catch (exc) {
      // Some pages lock callback properties.
    }
  }

  function wrapJsonpCallback(fn, endpoint, url) {
    var wrapped = function () {
      if (arguments.length > 0) {
        handleResponse(endpoint, url, arguments[0], "jsonp");
      }
      return fn.apply(this, arguments);
    };
    wrapped.__srunPresetCaptureWrapped = true;
    return wrapped;
  }

  function patchXhr() {
    var proto = window.XMLHttpRequest && window.XMLHttpRequest.prototype;
    var originalOpen;
    var originalSend;
    if (!proto) {
      return;
    }
    originalOpen = proto.open;
    originalSend = proto.send;

    proto.open = function (method, url) {
      this.__srunPresetCaptureUrl = String(url || "");
      this.__srunPresetCaptureMethod = String(method || "GET");
      return originalOpen.apply(this, arguments);
    };

    proto.send = function () {
      var xhr = this;
      var url = xhr.__srunPresetCaptureUrl || "";
      var body = arguments.length > 0 ? arguments[0] : null;
      if (isSrunUrl(url)) {
        handleRequest(url, "xhr", xhr.__srunPresetCaptureMethod || "GET", body);
        xhr.addEventListener("loadend", function () {
          handleResponse(endpointName(url), url, parseJsonpOrJson(xhr.responseText) || {
            status: xhr.status
          }, "xhr");
        });
      }
      return originalSend.apply(this, arguments);
    };
  }

  function patchFetch() {
    var originalFetch = window.fetch;
    if (!originalFetch) {
      return;
    }
    window.fetch = function (input, init) {
      var url = typeof input === "string" ? input : (input && input.url) || "";
      var method = (init && init.method) || (input && input.method) || "GET";
      var body = init && Object.prototype.hasOwnProperty.call(init, "body") ? init.body : null;
      var promise;
      if (isSrunUrl(url)) {
        if (body !== null && typeof body !== "undefined") {
          handleRequest(url, "fetch", method, body);
        } else if (input && typeof input !== "string" && typeof input.clone === "function") {
          try {
            input.clone().text().then(function (text) {
              handleRequest(url, "fetch", method, text);
            }, function () {
              handleRequest(url, "fetch", method, null);
            });
          } catch (exc) {
            handleRequest(url, "fetch", method, null);
          }
        } else {
          handleRequest(url, "fetch", method, null);
        }
      }
      promise = originalFetch.apply(this, arguments);
      if (isSrunUrl(url)) {
        promise.then(function (response) {
          try {
            response.clone().text().then(function (text) {
              handleResponse(endpointName(url), url, parseJsonpOrJson(text) || {
                status: response.status
              }, "fetch");
            });
          } catch (exc) {
            addNote("fetch 响应读取失败：" + String(exc && exc.message ? exc.message : exc));
          }
        });
      }
      return promise;
    };
  }

  function patchScriptInsertion() {
    var originalAppendChild = Node.prototype.appendChild;
    var originalInsertBefore = Node.prototype.insertBefore;
    var originalSetAttribute = HTMLScriptElement.prototype.setAttribute;
    var srcDescriptor = Object.getOwnPropertyDescriptor(HTMLScriptElement.prototype, "src");

    function inspectNode(node) {
      var src = node && node.tagName && String(node.tagName).toLowerCase() === "script" ?
        node.src || node.getAttribute("src") || "" :
        "";
      if (src && isSrunUrl(src)) {
        handleRequest(src, "jsonp-script", "GET");
      }
    }

    Node.prototype.appendChild = function (node) {
      inspectNode(node);
      return originalAppendChild.apply(this, arguments);
    };

    Node.prototype.insertBefore = function (node) {
      inspectNode(node);
      return originalInsertBefore.apply(this, arguments);
    };

    HTMLScriptElement.prototype.setAttribute = function (name, value) {
      if (String(name || "").toLowerCase() === "src" && isSrunUrl(value)) {
        handleRequest(String(value || ""), "jsonp-script", "GET");
      }
      return originalSetAttribute.apply(this, arguments);
    };

    if (srcDescriptor && srcDescriptor.set && srcDescriptor.get) {
      try {
        Object.defineProperty(HTMLScriptElement.prototype, "src", {
          configurable: true,
          enumerable: srcDescriptor.enumerable,
          get: function () {
            return srcDescriptor.get.call(this);
          },
          set: function (value) {
            if (isSrunUrl(value)) {
              handleRequest(String(value || ""), "jsonp-script", "GET");
            }
            srcDescriptor.set.call(this, value);
          }
        });
      } catch (exc) {
        // Older browsers may reject prototype descriptor changes.
      }
    }
  }

  function srunBase64Decode(input, alpha) {
    var clean = String(input || "").replace(/\s/g, "");
    var out = "";
    var idx;
    var c1;
    var c2;
    var c3;
    var c4;
    var b10;

    if (clean.length % 4 !== 0) {
      throw new Error("invalid custom base64 length");
    }

    for (idx = 0; idx < clean.length; idx += 4) {
      c1 = alpha.indexOf(clean.charAt(idx));
      c2 = alpha.indexOf(clean.charAt(idx + 1));
      c3 = clean.charAt(idx + 2) === "=" ? -1 : alpha.indexOf(clean.charAt(idx + 2));
      c4 = clean.charAt(idx + 3) === "=" ? -1 : alpha.indexOf(clean.charAt(idx + 3));

      if (c1 < 0 || c2 < 0 || (c3 < 0 && clean.charAt(idx + 2) !== "=") ||
          (c4 < 0 && clean.charAt(idx + 3) !== "=")) {
        throw new Error("invalid custom base64 character");
      }

      b10 = (c1 << 18) | (c2 << 12) | ((c3 < 0 ? 0 : c3) << 6) | (c4 < 0 ? 0 : c4);
      out += String.fromCharCode((b10 >> 16) & 255);
      if (clean.charAt(idx + 2) !== "=") {
        out += String.fromCharCode((b10 >> 8) & 255);
      }
      if (clean.charAt(idx + 3) !== "=") {
        out += String.fromCharCode(b10 & 255);
      }
    }
    return out;
  }

  function ordat(msg, idx) {
    return msg.length > idx ? msg.charCodeAt(idx) : 0;
  }

  function sencode(msg, includeLength) {
    var length = msg.length;
    var out = [];
    var idx;
    for (idx = 0; idx < length; idx += 4) {
      out.push(
        (ordat(msg, idx) |
          (ordat(msg, idx + 1) << 8) |
          (ordat(msg, idx + 2) << 16) |
          (ordat(msg, idx + 3) << 24)) >>> 0
      );
    }
    if (includeLength) {
      out.push(length);
    }
    return out;
  }

  function lencode(words, includeLength) {
    var length = words.length;
    var ll = (length - 1) << 2;
    var idx;
    var m;
    var out = "";

    if (includeLength) {
      m = words[length - 1];
      if (m < ll - 3 || m > ll) {
        return null;
      }
      ll = m;
    }

    for (idx = 0; idx < length; idx += 1) {
      out += String.fromCharCode(words[idx] & 255);
      out += String.fromCharCode((words[idx] >>> 8) & 255);
      out += String.fromCharCode((words[idx] >>> 16) & 255);
      out += String.fromCharCode((words[idx] >>> 24) & 255);
    }
    return includeLength ? out.substring(0, ll) : out;
  }

  function xxteaDecrypt(msg, key) {
    var v = sencode(msg, false);
    var k = sencode(key, false);
    var n = v.length - 1;
    var z;
    var y;
    var q;
    var sum;
    var e;
    var p;
    var mx;
    var DELTA = 0x9E3779B9 >>> 0;

    if (!msg) {
      return "";
    }
    while (k.length < 4) {
      k.push(0);
    }
    if (n < 1) {
      return lencode(v, true);
    }

    y = v[0];
    q = Math.floor(6 + 52 / (n + 1));
    sum = (q * DELTA) >>> 0;
    while (sum !== 0) {
      e = (sum >>> 2) & 3;
      for (p = n; p > 0; p -= 1) {
        z = v[p - 1];
        mx = (((z >>> 5) ^ (y << 2)) + (((y >>> 3) ^ (z << 4)) ^ (sum ^ y)) +
          (k[(p & 3) ^ e] ^ z)) >>> 0;
        y = v[p] = (v[p] - mx) >>> 0;
      }
      z = v[n];
      mx = (((z >>> 5) ^ (y << 2)) + (((y >>> 3) ^ (z << 4)) ^ (sum ^ y)) +
        (k[(p & 3) ^ e] ^ z)) >>> 0;
      y = v[0] = (v[0] - mx) >>> 0;
      sum = (sum - DELTA) >>> 0;
    }
    return lencode(v, true);
  }

  restoreState();
  state.page_origin = location.origin;
  state.page_pathname = location.pathname;
  if (looksLikePortalPage()) {
    detected = true;
    state.base_url = state.base_url || location.origin;
    state.ac_id = state.ac_id || queryParams(location.href).ac_id || "";
  }

  window.addEventListener("beforeunload", persistState);
  window.addEventListener("pagehide", persistState);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "hidden") {
      persistState();
    }
  });

  patchXhr();
  patchFetch();
  patchScriptInsertion();

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", ensurePanelIfRelevant);
  } else {
    ensurePanelIfRelevant();
  }
})();
