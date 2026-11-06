import argparse
import hashlib
import platform
import plistlib
import re
import shutil
import subprocess
import sys
from pathlib import Path

from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]
macos_directory = root / 'macos'
output = root / 'dist' / 'macos'
product_name = 'DuanjuApp'


def run(arguments, **kwargs):
    subprocess.run(arguments, cwd=kwargs.pop('cwd', root), check=True, **kwargs)


def configure_runner(flutter, variant):
    if not (macos_directory / 'Runner.xcodeproj').is_dir():
        run([flutter, 'create', '--platforms=macos', '--project-name', 'duanju_app',
             '--org', 'com.duanju', '--no-pub', str(root)])
    info_path = macos_directory / 'Runner' / 'Info.plist'
    info = plistlib.loads(info_path.read_bytes())
    info['CFBundleDisplayName'] = variant.name
    info_path.write_bytes(plistlib.dumps(info, sort_keys=True))
    config_path = macos_directory / 'Runner' / 'Configs' / 'AppInfo.xcconfig'
    config = config_path.read_text(encoding='utf-8')
    config = re.sub(r'^PRODUCT_NAME\s*=.*$', 'PRODUCT_NAME = ' + product_name,
                    config, flags=re.MULTILINE)
    config_path.write_text(config, encoding='utf-8')
    for name in ('DebugProfile.entitlements', 'Release.entitlements'):
        entitlements_path = macos_directory / 'Runner' / name
        entitlements = plistlib.loads(entitlements_path.read_bytes())
        entitlements['com.apple.security.app-sandbox'] = False
        entitlements['com.apple.security.network.client'] = True
        entitlements['com.apple.security.files.user-selected.read-write'] = True
        entitlements_path.write_bytes(plistlib.dumps(entitlements, sort_keys=True))


def build_native_library(variant):
    run([sys.executable, str(root / 'scripts' / 'build_native.py'),
         '--platform', 'darwin', *variant.arguments])
    library = root / 'native' / 'build' / 'darwin' / 'libduanju_core.dylib'
    if not library.is_file():
        raise SystemExit('本地核心 dylib 未生成')
    return library


def find_application():
    products = root / 'build' / 'macos' / 'Build' / 'Products' / 'Release'
    applications = sorted(products.glob('*.app'))
    if len(applications) != 1:
        raise SystemExit('未找到唯一的 macOS 应用：' + str(products))
    return applications[0]


def embed_library(application, library):
    frameworks = application / 'Contents' / 'Frameworks'
    frameworks.mkdir(parents=True, exist_ok=True)
    destination = frameworks / 'libduanju_core.dylib'
    shutil.copy2(library, destination)
    run(['install_name_tool', '-id', '@rpath/libduanju_core.dylib', str(destination)])
    symbols = subprocess.check_output(['nm', '-gU', str(destination)], text=True)
    for symbol in ('DuanjuRequest', 'DuanjuFree'):
        if symbol not in symbols:
            raise SystemExit('dylib 缺少 FFI 入口：' + symbol)
    run(['codesign', '--force', '--sign', '-', str(destination)])
    run(['codesign', '--force', '--deep', '--sign', '-', str(application)])
    run(['codesign', '--verify', '--deep', '--strict', str(application)])
    info = plistlib.loads((application / 'Contents' / 'Info.plist').read_bytes())
    executable = info.get('CFBundleExecutable', '')
    binary = application / 'Contents' / 'MacOS' / executable
    if not binary.is_file():
        raise SystemExit('应用缺少主程序：' + str(binary))
    for target in (destination, binary):
        architectures = subprocess.check_output(
            ['lipo', '-archs', str(target)], text=True).split()
        if 'arm64' not in architectures:
            raise SystemExit('产物缺少 arm64 架构：' + str(target))
        print(target.name + ': ' + ' '.join(architectures), flush=True)


def package(application, variant, version):
    output.mkdir(parents=True, exist_ok=True)
    stem = f'{variant.slug}-{version}-macos-arm64'
    archive_path = output / f'{stem}.zip'
    image_path = output / f'{stem}.dmg'
    archive_path.unlink(missing_ok=True)
    image_path.unlink(missing_ok=True)
    run(['ditto', '-c', '-k', '--sequesterRsrc', '--keepParent',
         str(application), str(archive_path)])
    staging = root / 'build' / 'macos' / 'dmg-staging'
    shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True)
    shutil.copytree(application, staging / application.name, symlinks=True)
    (staging / 'Applications').symlink_to('/Applications')
    run(['hdiutil', 'create', '-volname', variant.name, '-srcfolder', str(staging),
         '-ov', '-format', 'UDZO', str(image_path)])
    checksums = []
    for artifact in (archive_path, image_path):
        digest = hashlib.sha256()
        with artifact.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        checksums.append(f'{digest.hexdigest()}  {artifact.name}')
        print(artifact)
    (output / 'SHA256SUMS.txt').write_text('\n'.join(checksums) + '\n', encoding='ascii')
    return archive_path, image_path


def write_notes(variant, version, archive_path, image_path):
    lines = [
        f'# {variant.name} {version} · macOS Apple Silicon',
        '',
        '- 架构：arm64，仅支持 Apple Silicon（M 系列）Mac；Intel Mac 无法运行。',
        '- 编译版本：' + ('全剧视界（全站源）' if variant.all_sources else '短剧视界（仅红果）') + '。',
        '- 分发文件：`' + archive_path.name + '`（.app 压缩包）、`' + image_path.name + '`（安装镜像）。',
        '- 校验：下载 `SHA256SUMS.txt` 后执行 `shasum -a 256 -c SHA256SUMS.txt`。',
        '',
        '## 安装',
        '',
        '1. 解压 ZIP 或打开 DMG，把应用拖进“应用程序”。',
        '2. 应用未使用 Apple Developer ID 签名、未公证，首次打开请右键点击应用 → 打开 → 仍要打开；',
        '   或在终端执行 `xattr -dr com.apple.quarantine /Applications/' + product_name + '.app`。',
        '',
        '## 已知限制',
        '',
        '- 站源请求、浏览与播放由设备端 Go 核心提供，已随应用打包 `libduanju_core.dylib`。',
        '- 视频合并依赖的 FFmpeg 移动插件、后台下载服务等只有 Android / iOS 实现，macOS 上不可用。',
        '- 画质增强入口仅 Windows 开放，macOS 不显示。',
        '- 未签名的应用在系统升级后可能需要重新执行解除隔离命令。',
        '',
    ]
    (output / 'RELEASE_NOTES.md').write_text('\n'.join(lines), encoding='utf-8')


def main():
    parser = argparse.ArgumentParser(description='构建 macOS Apple Silicon 安装包')
    add_variant_argument(parser)
    options = parser.parse_args()
    variant = BuildVariant(options.all_sources)
    if platform.system() != 'Darwin':
        raise SystemExit('macOS 构建需要 macOS 和完整 Xcode。')
    if platform.machine() != 'arm64':
        raise SystemExit('此脚本只构建 arm64（Apple Silicon）版本。')
    flutter = shutil.which('flutter')
    if not flutter:
        raise SystemExit('请先安装 Flutter。')
    version = re.search(r'^version:\s*(\S+)',
                        (root / 'pubspec.yaml').read_text(encoding='utf-8'),
                        re.MULTILINE).group(1)
    configure_runner(flutter, variant)
    library = build_native_library(variant)
    run([flutter, 'pub', 'get'])
    run([flutter, 'build', 'macos', '--release', '--no-pub',
         *variant.flutter_arguments])
    application = find_application()
    embed_library(application, library)
    archive_path, image_path = package(application, variant, version)
    write_notes(variant, version, archive_path, image_path)


if __name__ == '__main__':
    main()
