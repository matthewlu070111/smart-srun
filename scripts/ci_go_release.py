#!/usr/bin/env python3
"""GitHub Actions orchestration for the pinned SDK builder and release exporter.

Python is a build-host tool. None of this file, the tests, or signing keys are
installed on a router. Publication consumes these exact verified packages.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
from types import SimpleNamespace
import zipfile

import build_go_sdk as sdk
import make_go_manifest as manifest
import verify_go_sdk as verifier

ROOT = Path(__file__).resolve().parents[1]
QEMU = {"amd64": None, "arm64": "qemu-aarch64", "arm": "qemu-arm",
        "mips": "qemu-mips", "mipsle": "qemu-mipsel"}


def run(args, **kwargs):
    return subprocess.check_output([str(a) for a in args], text=True, **kwargs).strip()


def catalog():
    return json.loads((ROOT / "targets.json").read_text())


def validate_version(version, channel=None):
    sdk.package_version(version, "opkg")
    if int(version.split('.')[0]) < 2:
        raise ValueError("Go releases start at version 2")
    if channel and ("rc" in version) != (channel == "rc"):
        raise ValueError("Version must match the selected release channel")
    return version


def metadata(args):
    validate_version(args.version, args.channel)
    data = catalog()
    selected = data['targets']
    if args.matrix == 'smoke':
        selected = [t for t in selected if t['openwrt_arch'] == 'x86_64']
    values = {'matrix': json.dumps({'target': [t['id'] for t in selected]}),
              'go_version': data['go_version'], 'version': args.version}
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        for key, value in values.items():
            output.write(key + '=' + value + '\n')


def export_apk_tool(apk, output, keys):
    # Relocatable OpenWrt SDKs wrap bin/apk in a shell script which refers to
    # ../lib and .apk.bin. The wrapper alone cannot run in the assembly job.
    # Both jobs use the same Ubuntu image; export the real host ELF and prove
    # that its verification and metadata commands work outside the SDK tree.
    binary = apk
    if binary.read_bytes()[:4] != b'\x7fELF':
        binary = apk.with_name('.apk.bin')
    if not binary.is_file() or binary.read_bytes()[:4] != b'\x7fELF':
        raise ValueError('SDK APK host tool is missing its native executable')
    exported = output / 'apk-tools'
    shutil.copyfile(binary, exported)
    exported.chmod(0o755)
    env = dict(os.environ)
    for key in ('LD_LIBRARY_PATH', 'LD_PRELOAD', 'RUNAS_ARG0'):
        env.pop(key, None)
    for package in sorted(output.glob('*.apk')):
        run([exported, '--keys-dir', keys, 'verify', package], cwd=output, env=env, timeout=30)
        run([exported, 'adbdump', '--format', 'json', package], cwd=output, env=env, timeout=30)


def build(args):
    validate_version(args.version)
    _, target = sdk.load_target(ROOT / 'targets.json', args.target)
    # Do not leave the secret in the environment inherited by SDK processes.
    private_pem = os.environ.pop('SMARTSRUN_APK_SIGNING_KEY', '')
    public_pem = os.environ.pop('SMARTSRUN_APK_PUBLIC_KEY', '')
    work = args.work.resolve()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    private = work / 'signing' / 'private.pem'
    public = work / 'trusted' / 'smart-srun.pem'
    signed = target['format'] == 'apk'
    try:
        if signed:
            private.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
            public.parent.mkdir(parents=True, exist_ok=True)
            if args.official:
                if not private_pem or not public_pem:
                    raise ValueError("Release signing requires SMARTSRUN_APK_SIGNING_KEY secret and SMARTSRUN_APK_PUBLIC_KEY variable in the release-signing environment")
                private.touch(mode=0o600)
                private.chmod(0o600)
                private.write_text(private_pem)
                public.write_text(public_pem)
            else:
                # Isolated CI keys cannot become release keys. Only public
                # keys leave this job; unsigned installation is never used.
                run(['openssl', 'ecparam', '-name', 'prime256v1', '-genkey', '-noout', '-out', private])
                private.chmod(0o600)
                public.write_text(run(['openssl', 'pkey', '-in', private, '-pubout']) + '\n')
            actual = subprocess.check_output(['openssl', 'pkey', '-in', str(private), '-pubout', '-outform', 'DER'])
            expected = subprocess.check_output(['openssl', 'pkey', '-pubin', '-in', str(public), '-outform', 'DER'])
            if actual != expected:
                raise ValueError("Release public key does not match the signing key")
        del private_pem
        sdk.build(SimpleNamespace(targets=ROOT/'targets.json', target=args.target, version=args.version,
                                  work_dir=work/'sdk', bootstrap=Path(run(['go', 'env', 'GOROOT'])),
                                  jobs=2, sign_key=private if signed else None, public_key=public if signed else None))
        target_work = work / 'sdk' / args.target
        artifacts = target_work / 'artifacts' / args.version
        apk = target_work / 'sdk' / 'staging_dir/host/bin/apk'
        emulator = QEMU[target['goarch']]
        qemu = shutil.which(emulator) if emulator else None
        if emulator and not qemu:
            raise ValueError('Missing version-smoke emulator: ' + emulator)
        evidence, inspection = verifier.verify(artifacts/'build-record.json', Path(shutil.which('go')),
                                              apk if signed else None, public.parent if signed else None,
                                              target['goarch'] == 'amd64', Path(qemu) if qemu else None)
        for path in artifacts.iterdir():
            if not path.is_file() or path.is_symlink():
                raise ValueError('Unexpected SDK artifact')
            shutil.copyfile(path, output/path.name)
        (output/'validation.json').write_text(json.dumps(evidence, indent=2)+'\n')
        (output/'inspection.json').write_text(json.dumps(inspection, indent=2)+'\n')
        if signed:
            shutil.copyfile(public, output/'apk-public.pem')
            # The exporter compares noarch APK metadata with the same native
            # verifier. This host tool remains an internal workflow artifact.
            if target['goarch'] == 'amd64':
                export_apk_tool(apk, output, public.parent)
        (output/'ci.json').write_text(json.dumps({'official_signing': args.official,
            'run_id': os.environ.get('GITHUB_RUN_ID'), 'repository': os.environ.get('GITHUB_REPOSITORY')})+'\n')
    finally:
        private.unlink(missing_ok=True)


def assemble(args):
    records = sorted(args.input.rglob('build-record.json'))
    required = {t['id'] for t in catalog()['targets']}
    observed, checks, keys, fingerprints = set(), {'schema_version': 1, 'assets': {}}, [], set()
    expected_source = run(['git', 'rev-parse', 'HEAD'], cwd=ROOT)
    for path in records:
        record = json.loads(path.read_text())
        target_id = record['target']['id']
        if target_id in observed or target_id not in required:
            raise ValueError('Duplicate or unexpected SDK target')
        observed.add(target_id)
        if record['source_commit'] != expected_source or record['source_dirty']:
            raise ValueError('Candidate does not come from this clean source commit')
        if record['display_version'] != args.version:
            raise ValueError('Candidate version mismatch')
        provenance = json.loads((path.parent/'ci.json').read_text())
        if provenance['official_signing'] != args.official:
            raise ValueError('Preview artifacts cannot be promoted to a release')
        if provenance['repository'] != os.environ.get('GITHUB_REPOSITORY') or provenance['run_id'] != os.environ.get('GITHUB_RUN_ID'):
            raise ValueError('Artifacts must come from this same workflow run')
        evidence = json.loads((path.parent/'validation.json').read_text())
        for digest, flags in evidence['assets'].items():
            if digest in checks['assets'] and checks['assets'][digest] != flags:
                raise ValueError('Conflicting validation evidence')
            checks['assets'][digest] = flags
        if record['target']['format'] == 'apk':
            keys.append(path.parent/'apk-public.pem')
            fingerprints.update(a['signature_spki_sha256'] for a in record['artifacts'])
    if observed != required:
        raise ValueError('Release candidate requires the complete pinned SDK matrix')
    if args.official and (os.environ.get('GITHUB_REPOSITORY') != manifest.REPOSITORY or len(fingerprints) != 1):
        raise ValueError('Official release requires the upstream repository and one maintainer signing key')
    trust = args.input / 'trusted-public-keys'
    trust.mkdir(exist_ok=False)
    for index, key in enumerate(keys):
        shutil.copyfile(key, trust/f'{index}.pem')
    tools = sorted(args.input.rglob('apk-tools'))
    if len(tools) != 1:
        raise ValueError('Expected one verified-build APK host tool')
    tools[0].chmod(0o755)
    release = manifest.export_release(records, args.output, checks, apk=tools[0], keys=trust)
    add_release_extras(release, records, keys, fingerprints, args)


def add_release_extras(release, records, keys, fingerprints, args):
    output = args.output
    assets = release['assets']
    # Each zip is an exact core + architecture-neutral LuCI combination for
    # the same package manager and SDK. It is not a second source of metadata.
    for core in [a for a in assets if a['kind'] == 'core']:
        matches = [a for a in assets if a['kind'] == 'luci' and a['package_manager'] == core['package_manager']
                   and a['sdk_release'] == core['sdk_release']]
        if len(matches) != 1:
            raise ValueError('Missing unambiguous matching LuCI package')
        name = f"smart-srun-split-{release['release']}-{core['openwrt_arch']}-{core['package_manager']}-{core['sdk_release']}.zip"
        with zipfile.ZipFile(output/name, 'x', compression=zipfile.ZIP_STORED) as archive:
            for asset in [core, matches[0]]:
                file = asset['url'].rsplit('/', 1)[1]
                archive.write(output/file, arcname=file)
    with tarfile.open(output/'build-records.tar.gz', 'x:gz') as archive:
        for path in records:
            target = json.loads(path.read_text())['target']['id']
            for filename in ['build-record.json', 'validation.json', 'inspection.json', 'ci.json']:
                archive.add(path.parent/filename, arcname=target+'/'+filename)
    if args.official:
        shutil.copyfile(keys[0], output/'smart-srun-apk.pem')
    template = ROOT/'.github'/'release-template.md'
    text = template.read_text(encoding='utf-8')
    replacements = {'VERSION': release['release'], 'SOURCE_COMMIT': release['source_commit'],
                    'APK_FINGERPRINT': ', '.join(sorted(fingerprints)),
                    'ASSET_COUNT': str(len(assets))}
    for key, value in replacements.items():
        text = text.replace('${'+key+'}', value)
    if '${' in text:
        raise ValueError('Unresolved release template fields')
    (output/'release-notes.md').write_text(text, encoding='utf-8')
    # Include manifest, keys, provenance and split archives as well as packages.
    sums = ''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n'
                   for p in sorted(output.iterdir()) if p.name != 'SHA256SUMS')
    (output/'SHA256SUMS').write_text(sums, encoding='utf-8')


def verify_published(args):
    """Publication is incomplete until GitHub retains every expected name/byte."""
    candidate = args.candidate
    expected = {p.name: (p.stat().st_size, 'sha256:' + hashlib.sha256(p.read_bytes()).hexdigest())
                for p in candidate.iterdir() if p.is_file()}
    release = json.loads((candidate / 'release-manifest.json').read_text())
    published = json.loads(args.metadata.read_text())
    if (published.get('draft') is not True
            or published.get('prerelease') != (release['channel'] == 'rc')
            or published.get('tag_name') != release['release']
            or published.get('target_commitish') != release['source_commit']):
        raise ValueError('Published draft identity differs from the verified candidate')
    assets = published.get('assets', [])
    actual = {a['name']: (a['size'], a.get('digest')) for a in assets}
    if len(actual) != len(assets) or actual != expected:
        raise ValueError('Published asset names, sizes or SHA256 digests differ from the candidate')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    meta = sub.add_parser('metadata')
    meta.add_argument('--matrix', choices=['all', 'smoke'], default='all')
    meta.add_argument('--channel', choices=['rc', 'stable'])
    builder = sub.add_parser('build')
    builder.add_argument('--target', required=True)
    builder.add_argument('--work', type=Path, required=True)
    builder.add_argument('--output', type=Path, required=True)
    exporter = sub.add_parser('assemble')
    exporter.add_argument('--input', type=Path, required=True)
    exporter.add_argument('--output', type=Path, required=True)
    published = sub.add_parser('verify-published')
    published.add_argument('--candidate', type=Path, required=True)
    published.add_argument('--metadata', type=Path, required=True)
    for command in [meta, builder, exporter]:
        command.add_argument('--version', required=True)
    for command in [builder, exporter]:
        command.add_argument('--official', action='store_true')
    args = parser.parse_args()
    {'metadata': metadata, 'build': build, 'assemble': assemble,
     'verify-published': verify_published}[args.command](args)


if __name__ == '__main__':
    main()
