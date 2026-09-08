"""The selected wireless security must reach the router without stale passwords."""

from pathlib import Path
import shutil
import subprocess
import unittest


ROOT = Path(__file__).resolve().parents[1]


class WizardWifiEncryptionTests(unittest.TestCase):
    def test_selection_controls_password_and_connection_payload(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node is not installed")
        script = r"""
const fs=require('fs'),vm=require('vm'),assert=require('assert');
const source=fs.readFileSync(process.argv[1],'utf8');let root,posted;
function find(el,predicate){if(predicate(el))return el;for(const child of el.children){const match=find(child,predicate);if(match)return match;}return null;}
function element(tag){return {tagName:tag.toUpperCase(),children:[],value:'',style:{},textContent:'',
  appendChild(child){this.children.push(child);child.parentNode=this;return child;},
  setAttribute(){},removeChild(child){this.children.splice(this.children.indexOf(child),1);},
  querySelector(selector){return find(this,el=>el.id===selector.slice(1));},focus(){}};}
const context={document:{createElement:element,createTextNode:text=>Object.assign(element('text'),{textContent:text}),
  getElementById:id=>find(root,el=>el.id===id)},schoolPresetList:()=>[],loadCustomPresets:()=>[]};
vm.createContext(context);vm.runInContext(source.slice(source.indexOf('  var WIZ_STEPS'),source.indexOf('  function initAll()')),context);
context.wizRender=()=>{root=element('div');context.wiz.root=root;context.wizStepEnv(root);};
context.wizPost=(endpoint,payload)=>{posted={endpoint,payload};};
context.wizReset();Object.assign(context.wiz,{accessMode:'wifi',ssid:'Campus'});context.wizRender();
const byId=id=>context.document.getElementById(id);
const choose=value=>{const select=byId('wiz-wifi-encryption');select.value=value;select.onchange();};
assert.strictEqual(context.wiz.wifiEncryption,'auto');
assert.deepStrictEqual(byId('wiz-wifi-encryption').children.map(el=>el.value),['auto','none','psk2','sae','sae-mixed','psk-mixed','psk']);
assert.strictEqual(byId('wiz-wifi-key').type,'password');
byId('wiz-wifi-key').value='private-wifi';byId('wiz-wifi-key').oninput();
choose('sae-mixed');assert.strictEqual(byId('wiz-wifi-key').value,'private-wifi');
context.wizConnectWifi();assert.strictEqual(posted.endpoint,'setup_wifi');
assert.strictEqual(posted.payload.encryption,'sae-mixed');assert.strictEqual(posted.payload.key,'private-wifi');
context.wiz.wifiJob='';context.wiz.busy='';context.wiz.env={state:'online'};
choose('none');assert.strictEqual(context.wiz.wifiKey,'');assert.strictEqual(byId('wiz-wifi-key'),null);
assert.strictEqual(context.wiz.env,null);context.wizConnectWifi();
assert.strictEqual(posted.payload.encryption,'none');assert.strictEqual(posted.payload.key,'');
context.wiz.wifiJob='';context.wiz.busy='';choose('psk2');
assert.strictEqual(byId('wiz-wifi-key').value,'','cleared password must not return');
context.wizGo(1);context.wizGo(0);assert.strictEqual(context.wiz.wifiEncryption,'psk2');
"""
        result = subprocess.run([node, "-e", script, str(ROOT / "root/www/luci-static/resources/smart_srun.js")],
                                stdin=subprocess.DEVNULL, capture_output=True, text=True, encoding="utf-8")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_luci_passes_encryption_to_private_wifi_payload(self):
        source = (ROOT / "root/usr/lib/lua/luci/controller/smart_srun.lua").read_text(encoding="utf-8")
        endpoint = source.split("function action_setup_wifi()", 1)[1].split("function action_discover_operators", 1)[0]
        self.assertIn('encryption = fv("encryption")', endpoint)
