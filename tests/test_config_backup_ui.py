"""Exercise the shared backup UI with delayed replies and uncertain writes."""
import shutil
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

class BackupUITests(unittest.TestCase):
    def test_lua_backup_routes_preserve_raw_input_and_cas(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua unavailable")
        result = subprocess.run([lua, str(ROOT/"tests/lua/controller_backup.lua"), str(ROOT)], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_preview_races_commit_token_and_download(self):
        node = shutil.which("node")
        if not node:
            self.skipTest("node unavailable")
        script = r"""
const fs = require('fs'), vm = require('vm'), assert = require('assert');
let source = fs.readFileSync(process.argv[1], 'utf8');
const end = source.lastIndexOf('})();');
source = source.slice(0,end) + '\nwindow.initBackupTest = initConfigBackup;\n' + source.slice(end);
const nodes={}, requests=[], readers=[], timers=[], downloads=[];
for (const id of ['backup','file','import','export','result']) {
 nodes['smart-srun-config-'+id] = {attributes:{}, disabled:false,
   getAttribute(k){return this.attributes[k];}, setAttribute(k,v){this.attributes[k]=v;}};
}
function XHR(){requests.push(this);}
XHR.prototype.open=function(method,url){this.method=method;this.url=url;};
XHR.prototype.setRequestHeader=function(){};
XHR.prototype.send=function(body){this.body=new URLSearchParams(body);};
XHR.prototype.reply=function(data){this.status=200;this.readyState=4;this.responseText=JSON.stringify(data);this.onreadystatechange();};
function FileReader(){readers.push(this);}
FileReader.prototype.readAsText=function(){};
const doc = {readyState:'loading', addEventListener(){},
 getElementById(id){return nodes[id]||null;}, querySelector(){return {value:'csrf-token'};},
 createElement(){return {click(){downloads.push(this.download);}};},
 body:{appendChild(){},removeChild(){}}};
const ctx={window:{},document:doc,XMLHttpRequest:XHR,FileReader,URL:{createObjectURL(){return 'blob:backup';},revokeObjectURL(){}},Blob:function(){},setTimeout(fn){timers.push(fn);},JSON};
vm.runInNewContext(source,ctx);ctx.window.initBackupTest();
const file=nodes['smart-srun-config-file'], button=nodes['smart-srun-config-import'], result=nodes['smart-srun-config-result'];
function choose(text){file.files=[{size:text.length}];file.onchange();const reader=readers.at(-1);reader.result=text;reader.onload();return requests.at(-1);}
const old=choose('old-secret'), fresh=choose('new-secret');
old.reply({ok:true,expected_revision:'old',campus_accounts:99,hotspot_profiles:99});
assert.equal(button.disabled,true);assert(!String(result.textContent).includes('99'));
fresh.reply({ok:true,expected_revision:'15',campus_accounts:2,hotspot_profiles:1,warnings:['synthetic warning']});
assert.equal(button.disabled,false);assert(!result.textContent.includes('new-secret'));
button.onclick();const commit=requests.at(-1);
assert.equal(commit.method,'POST');assert.equal(commit.body.get('token'),'csrf-token');
assert.equal(commit.body.get('expected_revision'),'15');assert.equal(commit.body.get('data'),'new-secret');
assert.equal(file.disabled,true);const count=requests.length;button.onclick();assert.equal(requests.length,count);
commit.ontimeout();assert.equal(button.disabled,true);assert.equal(file.disabled,false);assert(result.textContent.includes('不要直接重复'));
commit.reply({ok:true,message:'late success'});assert(!result.textContent.includes('late success'));
nodes['smart-srun-config-export'].onclick();requests.at(-1).reply({format:'smart-srun-config',format_version:1,config:{}});
assert.deepEqual(downloads,['smart-srun-config.json']);
assert.equal(requests.at(-1).body.get('token'),'csrf-token');
file.files=[{size:524289}];file.onchange();assert.equal(button.disabled,true);assert(result.textContent.includes('512'));
"""
        result = subprocess.run([node, "-e", script, str(ROOT/"root/www/luci-static/resources/smart_srun.js")], capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)

if __name__ == "__main__":
    unittest.main()
