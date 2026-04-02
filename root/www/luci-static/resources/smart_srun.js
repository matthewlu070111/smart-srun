(function() {
  if (window.__smartSrunUiLoaded) return;
  window.__smartSrunUiLoaded = true;

  var campusData = [];
  var hotspotData = [];
  var modalType = '';
  var modalEditId = '';
  var modalSaveHandler = null;
  var RELEASES_API_URL = 'https://api.github.com/repos/matthewlu070111/smart-srun/releases/latest';
  var RELEASES_PAGE_URL = 'https://github.com/matthewlu070111/smart-srun/releases';

  function readText(id) {
    var node = document.getElementById(id);
    if (!node) return '';
    return node.value || node.textContent || '';
  }

  function readJson(id, fallbackValue) {
    try {
      var text = readText(id);
      return text ? JSON.parse(text) : fallbackValue;
    } catch (err) {
      return fallbackValue;
    }
  }

  function escapeHtml(value) {
    return String(value == null ? '' : value)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function fetchJson(url, callback) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onreadystatechange = function() {
      if (xhr.readyState !== 4) return;
      if (xhr.status !== 200) {
        callback(new Error('http_' + xhr.status));
        return;
      }
      try {
        callback(null, JSON.parse(xhr.responseText || '{}'));
      } catch (err) {
        callback(err);
      }
    };
    xhr.send(null);
  }

  function normalizeVersionText(value) {
    var text = String(value == null ? '' : value).trim();
    var match = text.match(/^v?([^-]+)-r?(\d+)$/);
    if (!match) {
      match = text.match(/^v?(\d+(?:\.\d+)+)$/);
      if (!match) return '';
      return { base: match[1], release: 0 };
    }
    return { base: match[1], release: parseInt(match[2], 10) || 0 };
  }

  function compareVersionParts(left, right) {
    var leftParts = String(left || '0').split('.');
    var rightParts = String(right || '0').split('.');
    var length = Math.max(leftParts.length, rightParts.length);
    for (var i = 0; i < length; i++) {
      var leftNum = parseInt(leftParts[i] || '0', 10) || 0;
      var rightNum = parseInt(rightParts[i] || '0', 10) || 0;
      if (leftNum !== rightNum) return leftNum - rightNum;
    }
    return 0;
  }

  function isRemoteNewer(localVersion, remoteTag) {
    var localInfo = normalizeVersionText(localVersion);
    var remoteInfo = normalizeVersionText(remoteTag);
    if (!localInfo || !remoteInfo) return false;
    var baseCompare = compareVersionParts(localInfo.base, remoteInfo.base);
    if (baseCompare !== 0) return baseCompare < 0;
    return (localInfo.release || 0) < (remoteInfo.release || 0);
  }

  function initVersionNotice() {
    var container = document.getElementById('smart-srun-version-info');
    var link = document.getElementById('smart-srun-version-link');
    var dot = document.getElementById('smart-srun-update-dot');
    if (!container || !link || !dot || window.__smartSrunVersionInit) return;
    window.__smartSrunVersionInit = true;

    var text = container.textContent || '';
    var match = text.match(/v\d[^\s]*/);
    var localVersion = match ? match[0] : '';
    link.href = RELEASES_PAGE_URL;
    if (!localVersion) return;

    fetchJson(RELEASES_API_URL, function(err, data) {
      if (err || !data || typeof data.tag_name !== 'string') return;
      if (!isRemoteNewer(localVersion, data.tag_name)) return;
      dot.style.display = 'inline-block';
      link.title = '发现新版本：' + data.tag_name;
    });
  }

  window.smartFetchJson = fetchJson;

  function openBlockingFeedback(action, requestedAt) {
    var result = document.getElementById('smart-srun-manual-result') || document.getElementById('smart-srun-switch-result');
    var logBox = E('pre', {
      'style': 'max-height:18rem;overflow:auto;margin:0;padding:.75rem;border:1px solid rgba(127,127,127,.28);background:rgba(127,127,127,.08);white-space:pre-wrap;word-break:break-word;'
    }, '等待后端反馈...');
    var titles = {
      manual_login: '正在登录',
      manual_logout: '正在登出',
      switch_hotspot: '正在切到热点',
      switch_campus: '正在切回校园网'
    };
    var tips = {
      manual_login: '正在执行登录流程，请勿关闭页面。',
      manual_logout: '正在执行登出流程，请稍候。',
      switch_hotspot: '正在切换到热点网络，请稍候。',
      switch_campus: '正在切换回校园网，请稍候。'
    };
    var tip = E('p', { 'style': 'margin:.5rem 0 1rem 0;' }, tips[action] || '正在执行网络动作，请稍候。');
    var footer = E('div', { 'class': 'right' });
    var closed = false;
    var timer = null;
    var progressButton = E('button', {
      'class': 'btn cbi-button',
      'disabled': 'disabled'
    }, '进行中');
    var forceButton = E('button', {
      'class': 'btn cbi-button cbi-button-remove',
      'click': function(ev) {
        ev.preventDefault();
        if (closed || forceButton.disabled) return;
        forceButton.disabled = true;
        if (result) result.textContent = '正在强制停止...';
        var xhr = new XMLHttpRequest();
        xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
        xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
        xhr.onreadystatechange = function() {
          if (xhr.readyState !== 4) return;
          var text = '已触发强制停止';
          if (xhr.status === 200) {
            try {
              var data = JSON.parse(xhr.responseText || '{}');
              if (typeof data.message === 'string' && data.message !== '')
                text = data.message;
            } catch (e) {}
          }
          unlock(text, false);
        };
        xhr.send('action=' + encodeURIComponent('force_stop'));
      }
    }, '强制停止');

    progressButton.addEventListener('click', function(ev) {
      if (progressButton.disabled) {
        ev.preventDefault();
        return;
      }
      L.hideModal();
      location.reload();
    });

    footer.appendChild(progressButton);
    footer.appendChild(forceButton);

    function setTerminalFooter() {
      progressButton.disabled = false;
      progressButton.textContent = '关闭返回';
      forceButton.disabled = true;
    }

    function unlock(text, success) {
      if (closed) return;
      closed = true;
      if (timer) window.clearInterval(timer);
      setTerminalFooter();
      if (result && text) result.textContent = text + (success ? ' 🎉' : ' ⚠');
    }

    function checkTerminal(statusData) {
      if (!statusData) return false;
      if (statusData.last_action !== action) return false;
      if ((statusData.last_action_ts || 0) < requestedAt) return false;
      if (statusData.action_result === 'forced') {
        unlock(statusData.status || '已强制停止', false);
        return true;
      }
      if (statusData.action_result === 'error') {
        unlock(statusData.status || '执行失败', false);
        return true;
      }
      if (statusData.action_result === 'ok') {
        unlock(statusData.status || '操作完成', true);
        return true;
      }
      return false;
    }

    function poll() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/log_tail?lines=200&format=friendly&since=' + encodeURIComponent(requestedAt) + '&_=' + Date.now(), function(err, logData) {
        if (!err && logData && typeof logData.log === 'string') {
          logBox.textContent = logData.log;
          logBox.scrollTop = logBox.scrollHeight;
        }
      });

      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, statusData) {
        if (err) return;
        checkTerminal(statusData);
      });
    }

    L.showModal(titles[action] || '正在执行动作', [ tip, logBox, footer ], 'cbi-modal');
    timer = window.setInterval(poll, 1000);
    poll();
  }

  window.smartOpenBlockingFeedback = openBlockingFeedback;

  function getFieldValue(id) {
    var node = document.getElementById('widget.' + id) || document.getElementById(id);
    return node ? node.value : '';
  }

  function renderPasswordField(containerId, fieldId, value) {
    var container = document.getElementById(containerId);
    if (!container) return;
    L.require('ui').then(function(ui) {
      var widget = new ui.Textfield(value || '', {
        id: fieldId,
        password: true,
        optional: true
      });
      return Promise.resolve(widget.render()).then(function(node) {
        container.innerHTML = '';
        container.appendChild(node);
      });
    });
  }

  function setRowDisabled(rowId, inputId, disabled) {
    var row = document.getElementById(rowId);
    var input = document.getElementById(inputId);
    if (!row || !input) return;
    input.disabled = !!disabled;
    row.style.opacity = disabled ? '0.55' : '1';
  }

  function updateCampusAccessModeUI() {
    var mode = document.getElementById('jm-access_mode');
    if (!mode) return;
    var wired = mode.value === 'wired';
    setRowDisabled('jm-ssid-row', 'jm-ssid', wired);
    setRowDisabled('jm-bssid-row', 'jm-bssid', wired);
    setRowDisabled('jm-radio-row', 'jm-radio', wired);
  }

  function showNativeModal(title, bodyHtml, afterOpen, onSave) {
    var body = document.createElement('div');
    body.innerHTML = bodyHtml;

    var buttonRow = document.createElement('div');
    buttonRow.className = 'right';

    var cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.className = 'btn cbi-button';
    cancelBtn.textContent = '取消';
    cancelBtn.onclick = function() { L.hideModal(); };

    var saveBtn = document.createElement('button');
    saveBtn.type = 'button';
    saveBtn.className = 'btn cbi-button cbi-button-save important';
    saveBtn.textContent = '保存';
    saveBtn.onclick = function() {
      if (typeof modalSaveHandler === 'function') modalSaveHandler();
    };

    buttonRow.appendChild(cancelBtn);
    buttonRow.appendChild(document.createTextNode(' '));
    buttonRow.appendChild(saveBtn);

    modalSaveHandler = onSave;
    L.showModal(title, [ body, buttonRow ], 'cbi-modal');
    if (typeof afterOpen === 'function') afterOpen();
  }

  function findById(items, id) {
    for (var i = 0; i < items.length; i++) {
      if (items[i].id === id) return items[i];
    }
    return null;
  }

  function currentSchoolMetadata() {
    var allSchools = readJson('smart-school-data', []);
    var curSchoolSel = document.getElementById('widget.cbid.smart_srun.main.school')
      || document.getElementById('cbid.smart_srun.main.school')
      || document.querySelector('select[name="cbid.smart_srun.main.school"]');
    var curSchool = curSchoolSel ? curSchoolSel.value : 'jxnu';
    for (var i = 0; i < allSchools.length; i++) {
      if (allSchools[i].short_name === curSchool) return allSchools[i];
    }
    return null;
  }

  function radioOptionsMarkup() {
    return readText('smart-radio-options');
  }

  window.smartSetDefault = function(kind, id) {
    var fd = new FormData();
    fd.append('action', 'set_default_' + kind);
    fd.append('id', id);
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() {
      var message = '已保存默认配置';
      if (xhr.status === 200) {
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          if (typeof data.message === 'string' && data.message !== '')
            message = data.message;
        } catch (e) {}
      }
      alert(message);
      location.reload();
    };
    xhr.send(fd);
  };

  window.smartDelete = function(kind, id) {
    if (!confirm('确定要删除此项吗？')) return;
    var fd = new FormData();
    fd.append('action', 'delete_' + kind);
    fd.append('id', id);
    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() { location.reload(); };
    xhr.send(fd);
  };

  window.smartEditCampus = function(id) {
    modalType = 'campus';
    modalEditId = id;
    var item = id ? findById(campusData, id) : {};
    var schoolObj = currentSchoolMetadata();
    var ops = (schoolObj && schoolObj.operators && schoolObj.operators.length) ? schoolObj.operators : [
      {id:'cmcc', label:'中国移动'}, {id:'ctcc', label:'中国电信'},
      {id:'cucc', label:'中国联通'}, {id:'xn', label:'校内网'}
    ];
    var noSuffixOps = (schoolObj && schoolObj.no_suffix_operators) ? schoolObj.no_suffix_operators : ['xn'];
    var opOptions = '';
    for (var oi = 0; oi < ops.length; oi++) {
      var selected = (ops[oi].id === (item.operator || ops[0].id)) ? ' selected' : '';
      var badge = ops[oi].verified ? ' [已验证]' : '';
      opOptions += '<option value="' + escapeHtml(ops[oi].id) + '"' + selected + '>' + escapeHtml(ops[oi].label + badge) + '</option>';
    }

    var bodyHtml =
      '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + escapeHtml(item.label || '') + '"></div>' +
      '<div class="smart-native-row"><label>学工号</label><input id="jm-user_id" value="' + escapeHtml(item.user_id || '') + '"></div>' +
      '<div class="smart-native-row"><label>运营商</label><select id="jm-operator">' + opOptions + '</select></div>' +
      '<div class="smart-native-row"><label>运营商后缀（留空则为默认）</label><input id="jm-operator_suffix" value="' + escapeHtml(item.operator_suffix || '') + '" placeholder=""></div>' +
      '<div class="smart-native-row"><label>接入方式</label><select id="jm-access_mode"><option value="wifi"' + (((item.access_mode || 'wifi') === 'wifi') ? ' selected' : '') + '>无线</option><option value="wired"' + ((item.access_mode === 'wired') ? ' selected' : '') + '>有线（WAN）</option></select></div>' +
      '<div class="smart-native-row"><label>密码</label><div id="jm-password-field"></div></div>' +
      '<div class="smart-native-row"><label>认证地址</label><input id="jm-base_url" value="' + escapeHtml(item.base_url || 'http://172.17.1.2') + '"></div>' +
      '<div class="smart-native-row"><label>AC_ID</label><input id="jm-ac_id" value="' + escapeHtml(item.ac_id || '1') + '"></div>' +
      '<div class="smart-native-row" id="jm-ssid-row"><label>校园网 SSID</label><input id="jm-ssid" value="' + escapeHtml(item.ssid || 'jxnu_stu') + '"></div>' +
      '<div class="smart-native-row" id="jm-bssid-row"><label>BSSID（留空则不锁定）</label><input id="jm-bssid" value="' + escapeHtml(item.bssid || '') + '"></div>' +
      '<div class="smart-native-row" id="jm-radio-row"><label>频段</label><select id="jm-radio">' + radioOptionsMarkup() + '</select></div>';

    function updateSuffixPlaceholder() {
      var opSel = document.getElementById('jm-operator');
      var sfx = document.getElementById('jm-operator_suffix');
      if (!opSel || !sfx) return;
      var opId = opSel.value;
      var isNoSuffix = false;
      for (var i = 0; i < noSuffixOps.length; i++) {
        if (noSuffixOps[i] === opId) {
          isNoSuffix = true;
          break;
        }
      }
      sfx.placeholder = isNoSuffix ? '(无后缀)' : ('留空则使用 "' + opId + '"');
    }

    showNativeModal(
      id ? '编辑校园网账号' : '新增校园网账号',
      bodyHtml,
      function() {
        document.getElementById('jm-radio').value = item.radio || '';
        document.getElementById('jm-access_mode').addEventListener('change', updateCampusAccessModeUI);
        document.getElementById('jm-operator').addEventListener('change', updateSuffixPlaceholder);
        updateCampusAccessModeUI();
        updateSuffixPlaceholder();
        renderPasswordField('jm-password-field', 'jm-password', item.password || '');
      },
      function() { window.smartModalSave(); }
    );
  };

  window.smartEditHotspot = function(id) {
    modalType = 'hotspot';
    modalEditId = id;
    var item = id ? findById(hotspotData, id) : {};
    var bodyHtml =
      '<div class="smart-native-row"><label>标签（选填）</label><input id="jm-label" value="' + escapeHtml(item.label || '') + '"></div>' +
      '<div class="smart-native-row"><label>SSID</label><input id="jm-ssid" value="' + escapeHtml(item.ssid || '') + '"></div>' +
      '<div class="smart-native-row"><label>加密方式</label><select id="jm-encryption"><option value="none"' + (item.encryption === 'none' ? ' selected' : '') + '>开放(none)</option><option value="psk"' + (item.encryption === 'psk' ? ' selected' : '') + '>WPA-PSK</option><option value="psk2"' + ((item.encryption === 'psk2' || !item.encryption) ? ' selected' : '') + '>WPA2-PSK</option><option value="psk-mixed"' + (item.encryption === 'psk-mixed' ? ' selected' : '') + '>WPA/WPA2</option><option value="sae"' + (item.encryption === 'sae' ? ' selected' : '') + '>WPA3-SAE</option><option value="sae-mixed"' + (item.encryption === 'sae-mixed' ? ' selected' : '') + '>WPA2/WPA3</option></select></div>' +
      '<div class="smart-native-row"><label>密码</label><div id="jm-key-field"></div></div>' +
      '<div class="smart-native-row"><label>频段</label><select id="jm-radio">' + radioOptionsMarkup() + '</select></div>';
    showNativeModal(
      id ? '编辑热点配置' : '新增热点配置',
      bodyHtml,
      function() {
        document.getElementById('jm-encryption').value = item.encryption || 'psk2';
        document.getElementById('jm-radio').value = item.radio || '';
        renderPasswordField('jm-key-field', 'jm-key', item.key || '');
      },
      function() { window.smartModalSave(); }
    );
  };

  window.smartModalSave = function() {
    var fd = new FormData();
    fd.append('action', (modalEditId ? 'edit_' : 'add_') + modalType);
    if (modalEditId) fd.append('id', modalEditId);

    if (modalType === 'campus') {
      fd.append('label', document.getElementById('jm-label').value);
      fd.append('user_id', document.getElementById('jm-user_id').value);
      fd.append('operator', document.getElementById('jm-operator').value);
      fd.append('operator_suffix', document.getElementById('jm-operator_suffix').value);
      fd.append('access_mode', document.getElementById('jm-access_mode').value);
      fd.append('password', getFieldValue('jm-password'));
      fd.append('base_url', document.getElementById('jm-base_url').value);
      fd.append('ac_id', document.getElementById('jm-ac_id').value);
      fd.append('ssid', document.getElementById('jm-ssid').value);
      fd.append('bssid', document.getElementById('jm-bssid').value);
      fd.append('radio', document.getElementById('jm-radio').value);
    } else {
      fd.append('label', document.getElementById('jm-label').value);
      fd.append('ssid', document.getElementById('jm-ssid').value);
      fd.append('encryption', document.getElementById('jm-encryption').value);
      fd.append('key', getFieldValue('jm-key'));
      fd.append('radio', document.getElementById('jm-radio').value);
    }

    var xhr = new XMLHttpRequest();
    xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
    xhr.onload = function() {
      L.hideModal();
      location.reload();
    };
    xhr.send(fd);
  };

  function initSchoolInfo() {
    var schools = readJson('smart-school-data', []);
    var infoBox = document.getElementById('smart-school-info');
    var descEl = document.getElementById('smart-school-desc');
    var descTextEl = document.getElementById('smart-school-desc-text');
    var contribEl = document.getElementById('smart-school-contrib');
    var contribTextEl = document.getElementById('smart-school-contrib-text');
    var helperEl = document.getElementById('smart-school-helper');
    var outerDescEl = null;
    if (!infoBox || !descEl || !descTextEl || !contribEl || !contribTextEl || !helperEl || window.__smartSchoolInfoInit) return;
    window.__smartSchoolInfoInit = true;

    for (var parent = infoBox.parentNode; parent; parent = parent.parentNode) {
      if (parent.className && String(parent.className).indexOf('cbi-value-description') >= 0) {
        outerDescEl = parent;
        break;
      }
    }

    function lookup(shortName) {
      for (var i = 0; i < schools.length; i++) {
        if (schools[i].short_name === shortName) return schools[i];
      }
      return null;
    }

    function isGithubUsername(value) {
      return /^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,37})$/.test(value || '') && !/--/.test(value || '') && !/-$/.test(value || '');
    }

    function clearNode(node) {
      while (node.firstChild) node.removeChild(node.firstChild);
    }

    function renderContributors(contributors) {
      clearNode(contribTextEl);
      for (var i = 0; i < contributors.length; i++) {
        var text = String(contributors[i] == null ? '' : contributors[i]);
        if (i > 0) contribTextEl.appendChild(document.createTextNode(', '));
        if (isGithubUsername(text)) {
          var link = document.createElement('a');
          link.href = 'https://github.com/' + text.substring(1);
          link.target = '_blank';
          link.rel = 'noopener noreferrer';
          link.textContent = text;
          contribTextEl.appendChild(link);
        } else {
          var span = document.createElement('span');
          span.textContent = text;
          contribTextEl.appendChild(span);
        }
      }
    }

    function sync(desc, contributors) {
      var hasContrib = contributors && contributors.length;
      infoBox.style.display = 'block';
      if (outerDescEl) outerDescEl.style.display = 'block';
      descEl.style.display = desc ? 'block' : 'none';
      contribEl.style.display = hasContrib ? 'block' : 'none';
      contribEl.style.marginTop = '4px';
      helperEl.style.marginTop = (desc || hasContrib) ? '4px' : '0';
      descTextEl.textContent = desc || '';
      renderContributors(hasContrib ? contributors : []);
    }

    function findSchoolSelect() {
      var node = infoBox;
      while (node) {
        if (node.className && String(node.className).indexOf('cbi-value-field') >= 0) {
          var inner = node.querySelector('select');
          if (inner) return inner;
          break;
        }
        node = node.parentNode;
      }
      return document.getElementById('widget.cbid.smart_srun.main.school')
        || document.getElementById('cbid.smart_srun.main.school')
        || document.querySelector('select[name="cbid.smart_srun.main.school"]');
    }

    var sel = findSchoolSelect();
    if (!sel) return;

    function update(value) {
      var school = lookup(value);
      if (!school) {
        sync('', []);
        return;
      }
      sync(school.description || '', (school.contributors && school.contributors.length) ? school.contributors : []);
    }

    update(sel.value);
    sel.addEventListener('change', function() { update(sel.value); });
  }

  function initOverview() {
    var root = document.getElementById('smart-srun-overview');
    var title = document.getElementById('smart-srun-overview-title');
    var meta = document.getElementById('smart-srun-overview-meta');
    if (!root || !title || !meta || window.__smartSrunOverviewInit) return;
    window.__smartSrunOverviewInit = true;

    var palette = {
      online: { border: '#2e7d32', bg: 'rgba(46,125,50,.10)', title: '#166534', meta: '#166534' },
      portal: { border: '#ef6c00', bg: 'rgba(239,108,0,.10)', title: '#b45309', meta: '#92400e' },
      limited: { border: '#c62828', bg: 'rgba(198,40,40,.10)', title: '#b91c1c', meta: '#991b1b' },
      offline: { border: '#6b7280', bg: 'rgba(107,114,128,.10)', title: '#374151', meta: '#4b5563' }
    };

    function applyTone(level) {
      var tone = palette[level] || palette.offline;
      root.style.borderLeftColor = tone.border;
      root.style.background = tone.bg;
      title.style.color = tone.title;
      meta.style.color = tone.meta;
    }

    function refreshOverview() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/status?_=' + Date.now(), function(err, data) {
        if (err) {
          applyTone('offline');
          title.textContent = '状态读取失败';
          meta.innerHTML = '<span>WiFi: --</span><span>模式: --</span><span>连通性: --</span>';
          return;
        }
        var level = (typeof data.connectivity_level === 'string' && data.connectivity_level !== '') ? data.connectivity_level : 'offline';
        var status = (typeof data.status === 'string' && data.status !== '') ? data.status : '未知';
        var ssid = (typeof data.current_ssid === 'string' && data.current_ssid !== '') ? data.current_ssid : '未连接';
        var mode = (typeof data.mode_label === 'string' && data.mode_label !== '') ? data.mode_label : '未知模式';
        var conn = (typeof data.connectivity === 'string' && data.connectivity !== '') ? data.connectivity : '未知';
        var iface = (typeof data.current_iface === 'string' && data.current_iface !== '') ? data.current_iface : '--';
        var ip = (typeof data.current_ip === 'string' && data.current_ip !== '') ? data.current_ip : '--';
        var pending = (typeof data.pending_action === 'string' && data.pending_action !== '') ? ('；待执行动作: ' + data.pending_action) : '';
        var campusLabel = (typeof data.online_account_label === 'string' && data.online_account_label !== '') ? data.online_account_label : ((typeof data.campus_account_label === 'string' && data.campus_account_label !== '') ? data.campus_account_label : '--');
        var hotspotLabel = (typeof data.hotspot_profile_label === 'string' && data.hotspot_profile_label !== '') ? data.hotspot_profile_label : '--';

        applyTone(level);
        title.textContent = status + pending;
        var metaHtml = '<span>WiFi: ' + escapeHtml(ssid) + '</span><span>模式: ' + escapeHtml(mode) + '</span><span>连通性: ' + escapeHtml(conn) + '</span><span>接口/IP: ' + escapeHtml(iface) + ' / ' + escapeHtml(ip) + '</span>';
        if (mode === '热点模式') {
          metaHtml += '<span>热点: ' + escapeHtml(hotspotLabel) + '</span>';
        } else {
          metaHtml += '<span>账号: ' + escapeHtml(campusLabel) + '</span>';
        }
        meta.innerHTML = metaHtml;
      });
    }

    refreshOverview();
    window.setInterval(refreshOverview, 1200);
  }

  function initManualActions() {
    var login = document.getElementById('smart-srun-manual-login');
    var logout = document.getElementById('smart-srun-manual-logout');
    var result = document.getElementById('smart-srun-manual-result');
    if (!login || !logout || !result || window.__smartSrunManualInit) return;
    window.__smartSrunManualInit = true;

    function submit(action) {
      result.textContent = '正在提交...';
      login.disabled = true;
      logout.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        login.disabled = false;
        logout.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '提交失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
          result.textContent = message;
          if (data.ok) {
            openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
          }
        } catch (e) {
          result.textContent = '提交失败';
        }
      };
      xhr.send('action=' + encodeURIComponent(action));
    }

    login.addEventListener('click', function() { submit('manual_login'); });
    logout.addEventListener('click', function() { submit('manual_logout'); });
  }

  function initSwitchActions() {
    var hotspot = document.getElementById('smart-srun-switch-hotspot');
    var campus = document.getElementById('smart-srun-switch-campus');
    var forceClose = document.getElementById('smart-srun-force-close');
    var result = document.getElementById('smart-srun-switch-result');
    if (!hotspot || !campus || !forceClose || !result || window.__smartSrunSwitchInit) return;
    window.__smartSrunSwitchInit = true;

    function enqueue(action) {
      result.textContent = '正在提交...';
      hotspot.disabled = true;
      campus.disabled = true;
      forceClose.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        hotspot.disabled = false;
        campus.disabled = false;
        forceClose.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '提交失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          var message = (typeof data.message === 'string' && data.message !== '') ? data.message : '已提交';
          result.textContent = message;
          if (data.ok) {
            openBlockingFeedback(action, parseInt(data.requested_at || 0, 10) || 0);
          }
        } catch (e) {
          result.textContent = '提交失败';
        }
      };
      xhr.send('action=' + encodeURIComponent(action));
    }

    function enqueueForceClose() {
      if (!confirm('这会停止 SMART SRun 服务并终止插件进程，是否继续？')) {
        return;
      }
      result.textContent = '正在强制关闭插件...';
      hotspot.disabled = true;
      campus.disabled = true;
      forceClose.disabled = true;

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/cgi-bin/luci/admin/services/smart_srun/enqueue', true);
      xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
      xhr.onreadystatechange = function() {
        if (xhr.readyState !== 4) return;
        hotspot.disabled = false;
        campus.disabled = false;
        forceClose.disabled = false;
        if (xhr.status !== 200) {
          result.textContent = '强制关闭失败';
          return;
        }
        try {
          var data = JSON.parse(xhr.responseText || '{}');
          result.textContent = (typeof data.message === 'string' && data.message !== '') ? data.message : '已强制关闭插件';
          if (data.ok) {
            location.reload();
          }
        } catch (e) {
          result.textContent = '强制关闭失败';
        }
      };
      xhr.send('action=' + encodeURIComponent('force_stop'));
    }

    hotspot.addEventListener('click', function() { enqueue('switch_hotspot'); });
    campus.addEventListener('click', function() { enqueue('switch_campus'); });
    forceClose.addEventListener('click', enqueueForceClose);
  }

  function initTables() {
    if (window.__smartTablesInit) return;
    if (!document.getElementById('smart-campus-data') || !document.getElementById('smart-hotspot-data')) return;
    window.__smartTablesInit = true;
    campusData = readJson('smart-campus-data', []);
    hotspotData = readJson('smart-hotspot-data', []);
  }

  function initLogView() {
    var box = document.getElementById('smart-srun-log-box');
    var pre = document.getElementById('smart-srun-log-pre');
    var toggle = document.getElementById('smart-srun-refresh-toggle');
    if (!box || !pre || !toggle || window.__smartSrunLogInit) return;
    window.__smartSrunLogInit = true;
    var autoRefresh = true;
    var timer = null;

    function atBottom() {
      return (box.scrollHeight - box.scrollTop - box.clientHeight) < 24;
    }

    function stickBottom() {
      box.scrollTop = box.scrollHeight;
    }

    function setToggleText() {
      toggle.textContent = autoRefresh ? '刷新: 开' : '刷新: 关';
    }

    function refresh() {
      fetchJson('/cgi-bin/luci/admin/services/smart_srun/log_tail?lines=80&format=friendly&_=' + Date.now(), function(err, data) {
        if (err || typeof data.log !== 'string') return;
        var keepBottom = atBottom();
        pre.textContent = data.log;
        if (keepBottom) stickBottom();
      });
    }

    function startLoop() {
      if (timer) clearInterval(timer);
      timer = setInterval(function() {
        if (autoRefresh) refresh();
      }, 2000);
    }

    toggle.addEventListener('click', function() {
      autoRefresh = !autoRefresh;
      setToggleText();
      if (autoRefresh) refresh();
    });

    setToggleText();
    stickBottom();
    startLoop();
  }

  function initAll() {
    initVersionNotice();
    initTables();
    initSchoolInfo();
    initOverview();
    initManualActions();
    initSwitchActions();
    initLogView();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initAll);
  } else {
    initAll();
  }
})();
