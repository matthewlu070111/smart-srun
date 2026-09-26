"""Release automation must refuse wrong identities and preserve verified bytes."""
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'scripts'))
import ci_go_release as release  # noqa: E402
import check_go_source as source  # noqa: E402


class ReleaseAutomationTests(unittest.TestCase):
    def test_apk_export_unwraps_sdk_launcher_and_checks_relocated_tool(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            sdk, output = root/'sdk', root/'artifact'
            sdk.mkdir()
            output.mkdir()
            apk = sdk/'apk'
            apk.write_text('#!/bin/sh\nexec "$0.missing" "$@"\n')
            native = b'\x7fELF synthetic host executable'
            (sdk/'.apk.bin').write_bytes(native)
            package = output/'luci.apk'
            package.write_bytes(b'synthetic signed archive')
            loader = {name: 'sdk-only' for name in ('LD_LIBRARY_PATH', 'LD_PRELOAD', 'RUNAS_ARG0')}
            with patch.dict(os.environ, loader), patch.object(release, 'run') as run:
                release.export_apk_tool(apk, output, root/'trusted')
            self.assertEqual((output/'apk-tools').read_bytes(), native)
            self.assertEqual(run.call_count, 2)
            self.assertEqual(run.call_args_list[0].args[0],
                             [output/'apk-tools', '--keys-dir', root/'trusted', 'verify', package])
            for call in run.call_args_list:
                self.assertEqual(call.kwargs['cwd'], output)
                self.assertTrue(set(loader).isdisjoint(call.kwargs['env']))
            with patch.object(release, 'run', side_effect=RuntimeError('cannot verify')):
                with self.assertRaisesRegex(RuntimeError, 'cannot verify'):
                    release.export_apk_tool(apk, output, root/'trusted')
            (sdk/'.apk.bin').unlink()
            with self.assertRaisesRegex(ValueError, 'missing its native executable'):
                release.export_apk_tool(apk, output, root/'trusted')

    def test_versions_reject_shell_paths_and_wrong_channels(self):
        for value in ['1.6.0', 'v2.0.0', '2.0.0rc0', '2.0.0;echo bad', '../../2.0.0', '2.0.0\n']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                release.validate_version(value)
        self.assertEqual(release.validate_version('2.0.0rc10', 'rc'), '2.0.0rc10')
        self.assertEqual(release.validate_version('2.0.0', 'stable'), '2.0.0')
        with self.assertRaises(ValueError):
            release.validate_version('2.0.0rc2', 'stable')

    def test_official_apk_without_key_fails_before_build(self):
        target = next(t for t in release.catalog()['targets'] if t['format'] == 'apk')
        with tempfile.TemporaryDirectory() as temp, patch.dict(os.environ, {}, clear=True), patch.object(release.sdk, 'build') as build:
            args = SimpleNamespace(version='2.0.0rc1', target=target['id'], official=True,
                                   work=Path(temp)/'work', output=Path(temp)/'out')
            with self.assertRaisesRegex(ValueError, 'requires SMARTSRUN_APK_SIGNING_KEY'):
                release.build(args)
            build.assert_not_called()
            self.assertFalse((args.work/'signing/private.pem').exists())

    def test_missing_matrix_and_preview_promotion_fail_closed(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(release, 'run', return_value='a'*40):
            root = Path(temp)
            args = SimpleNamespace(input=root, output=root/'release', version='2.0.0rc1', official=True)
            with self.assertRaisesRegex(ValueError, 'complete pinned SDK matrix'):
                release.assemble(args)
            folder = root/'one'
            folder.mkdir()
            (folder/'build-record.json').write_text(json.dumps({
                'source_commit': 'a'*40, 'source_dirty': False, 'display_version': args.version,
                'target': release.catalog()['targets'][0]}))
            (folder/'ci.json').write_text(json.dumps({'official_signing': False}))
            with self.assertRaisesRegex(ValueError, 'Preview artifacts'):
                release.assemble(args)
            self.assertFalse(args.output.exists())

    def test_split_zip_and_all_extra_checksums_preserve_exact_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            out = root/'out'
            out.mkdir()
            records = root/'records'
            records.mkdir()
            (records/'build-record.json').write_text(json.dumps({'target': {'id': 'synthetic-target'}}))
            for name in ['validation.json', 'inspection.json', 'ci.json']:
                (records/name).write_text('{}')
            key = root/'public.pem'
            key.write_text('synthetic public key; not usable for installation')
            assets = []
            for kind in ['core', 'luci']:
                filename = kind+'.ipk'
                (out/filename).write_bytes((kind+' exact bytes').encode())
                assets.append({'kind': kind, 'package_manager': 'opkg', 'sdk_release': '24.10.8',
                               'openwrt_arch': 'x86_64' if kind == 'core' else 'all',
                               'url': 'https://example.test/'+filename})
            data = {'release': '2.0.0rc1', 'source_commit': 'a'*40, 'assets': assets}
            release.add_release_extras(data, [records/'build-record.json'], [key], {'b'*64},
                                      SimpleNamespace(output=out, official=True))
            with zipfile.ZipFile(next(out.glob('*.zip'))) as archive:
                self.assertEqual(archive.namelist(), ['core.ipk', 'luci.ipk'])
                self.assertEqual(archive.read('core.ipk'), b'core exact bytes')
            for line in (out/'SHA256SUMS').read_text().splitlines():
                digest, name = line.split('  ')
                self.assertEqual(digest, hashlib.sha256((out/name).read_bytes()).hexdigest())
            self.assertNotIn('${', (out/'release-notes.md').read_text(encoding='utf-8'))

    def test_source_audit_allows_host_tools_but_rejects_runtime_and_private_files(self):
        paths = ['scripts/ci_go_release.py', 'tests/test_ci_go_release.py', 'root/usr/lib/smart_srun/cli.py',
                 '.codex/private.json', 'root/etc/smart-srun/config.json', 'signing.key', 'old.apk']
        self.assertEqual(source.inspect(paths), sorted(paths[2:]))

    def test_published_draft_checks_server_names_and_digests(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            candidate = root / 'candidate'
            candidate.mkdir()
            data = {'release': '2.0.0rc1', 'source_commit': 'a' * 40, 'channel': 'rc'}
            (candidate / 'release-manifest.json').write_text(json.dumps(data))
            (candidate / 'smart-srun_2.0.0.rc1-r1_x86_64.ipk').write_bytes(b'exact package bytes')
            assets = [{'name': p.name, 'size': p.stat().st_size,
                       'digest': 'sha256:' + hashlib.sha256(p.read_bytes()).hexdigest()}
                      for p in candidate.iterdir()]
            original = dict(draft=True, prerelease=True, tag_name=data['release'],
                            target_commitish=data['source_commit'], assets=assets)
            path = root / 'github.json'
            args = SimpleNamespace(candidate=candidate, metadata=path)
            path.write_text(json.dumps(original))
            release.verify_published(args)
            for field, value in [('name', 'github-renamed.ipk'), ('size', 0), ('digest', None)]:
                with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'asset names'):
                    changed = json.loads(json.dumps(original))
                    changed['assets'][0][field] = value
                    path.write_text(json.dumps(changed))
                    release.verify_published(args)
            for field, value in [('draft', False), ('prerelease', False), ('tag_name', 'wrong'),
                                 ('target_commitish', 'b' * 40)]:
                with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'draft identity'):
                    changed = dict(original, **{field: value})
                    path.write_text(json.dumps(changed))
                    release.verify_published(args)

    def test_workflows_pin_actions_and_use_verified_candidate(self):
        import yaml
        for path in (ROOT/'.github/workflows').glob('*.yml'):
            workflow = yaml.safe_load(path.read_text())
            for job in workflow['jobs'].values():
                for step in job.get('steps', []):
                    uses = step.get('uses', '')
                    if uses and not uses.startswith('./'):
                        self.assertRegex(uses, r'^[\w/-]+@[0-9a-f]{40}$')
        publish = (ROOT/'.github/workflows/publish-go.yml').read_text()
        self.assertIn('sha256sum --strict --check', publish)
        self.assertIn('--draft --latest=false', publish)
        self.assertNotIn('--clobber', publish)
        self.assertNotIn('build_go_sdk.py', publish)
        self.assertIn('verify-published', publish)

    def test_release_callers_forward_signing_context_only_to_builder(self):
        import yaml
        workflows = ROOT / '.github/workflows'
        builder = yaml.safe_load((workflows / 'build-go.yml').read_text())
        # PyYAML's YAML 1.1 reader treats the GitHub Actions `on` key as true.
        triggers = builder.get('on', builder.get(True))
        self.assertIs(triggers['workflow_call']['secrets']['SMARTSRUN_APK_SIGNING_KEY']['required'], False)
        self.assertIn('release-signing', builder['jobs']['build']['environment'])
        for name in ('build-release.yml', 'build-prerelease.yml'):
            with self.subTest(workflow=name):
                jobs = yaml.safe_load((workflows / name).read_text())['jobs']
                self.assertEqual(jobs['candidate']['secrets'], 'inherit')
                self.assertEqual(jobs['candidate']['uses'], './.github/workflows/build-go.yml')
                self.assertNotIn('secrets', jobs['publish'])


if __name__ == '__main__':
    unittest.main()
