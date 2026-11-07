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

# 关闭窗口会触发退出。界面引擎的优雅关闭要等待仍在跑的 isolate，
# 站源/海报请求超时时主线程会一直卡在 NSApplication terminate:。
# 这里只向模板追加退出方法，保留模板原有的入口标记与插件注册。
MACOS_TERMINATION_METHODS = '''
  // 退出前先让 Go 核心取消在途请求并落盘，然后直接结束进程。
  override func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
    stopNativeCore()
    exit(0)
  }

  private func stopNativeCore() {
    let path = Bundle.main.bundlePath + "/Contents/Frameworks/libduanju_core.dylib"
    guard let handle = dlopen(path, RTLD_NOW) else { return }
    typealias RequestFunction = @convention(c) (UnsafePointer<CChar>?) -> UnsafeMutablePointer<CChar>?
    typealias FreeFunction = @convention(c) (UnsafeMutablePointer<CChar>?) -> Void
    guard let requestSymbol = dlsym(handle, "DuanjuRequest"),
          let freeSymbol = dlsym(handle, "DuanjuFree") else { return }
    let request = unsafeBitCast(requestSymbol, to: RequestFunction.self)
    let free = unsafeBitCast(freeSymbol, to: FreeFunction.self)
    let payload = #"{"action":"shutdown"}"#
    if let result = payload.withCString({ request($0) }) {
      free(result)
    }
  }
'''


def run(arguments, **kwargs):
    subprocess.run(arguments, cwd=kwargs.pop('cwd', root), check=True, **kwargs)


def configure_runner(flutter, variant):
    if not (macos_directory / 'Runner.xcodeproj').is_dir():
        run([flutter, 'create', '--platforms=macos', '--project-name', 'duanju_app',
             '--org', 'com.duanju', '--no-pub', str(root)])
    app_delegate_path = macos_directory / 'Runner' / 'AppDelegate.swift'
    template = app_delegate_path.read_text(encoding='utf-8')
    if 'applicationShouldTerminate(' not in template:
        body = template.rstrip()
        closing = body.rfind('}')
        if closing < 0:
            raise SystemExit('macOS AppDelegate.swift 结构不符合预期')
        patched = body[:closing] + MACOS_TERMINATION_METHODS + body[closing:] + '\n'
        if 'import Darwin' not in patched:
            patched = patched.replace('import FlutterMacOS',
                                      'import FlutterMacOS\nimport Darwin', 1)
        app_delegate_path.write_text(patched, encoding='utf-8')
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
        '## 修复记录',
        '',
        '- 切换代理模式或代理地址时清除站源请求的 403 退避，并唤醒等待中的请求，避免沿用旧网络出口下的失败结论。',
        '- 用户主动发起的站源健康检测强制重新发起请求，不再被上一次失败留下的退避拦截；后台更新仍受限流约束。',
        '- 修复点关闭窗口或退出时卡死：退出前取消所有在途站源/海报请求并落盘，再结束进程，不再等待界面引擎的优雅关闭。',
        '- 海报域名连续请求失败后临时跳过，避免连接超时占满并发槽导致界面无响应；强退遗留的「更新中」状态会在退出时清除。',
        '',
        '## 已知限制',
        '',
        '- 站源请求、浏览与播放由设备端 Go 核心提供，已随应用打包 `libduanju_core.dylib`。',
        '- 应用主程序是 arm64 / x86_64 通用二进制，但内置 Go 核心库仅 arm64，请勿在 Intel Mac 上使用。',
        '- 后台下载、画质增强等移动端 / Windows 专属功能未在 macOS 上做完整适配。',
        '- 视频合并等依赖原生插件的能力未在 macOS 版上做完整验证。',
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
