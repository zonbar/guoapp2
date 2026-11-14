cmake_minimum_required(VERSION 3.14)

option(MEDIA_KIT_LIBS_AVAILABLE "package:media_kit libraries are available." ON)
set(PROJECT_NAME "media_kit_libs_windows_video")
project(${PROJECT_NAME} LANGUAGES CXX)
add_compile_definitions(_DISABLE_CONSTEXPR_MUTEX_CONSTRUCTOR)

if(NOT CMAKE_SIZEOF_VOID_P EQUAL 8 OR FLUTTER_TARGET_PLATFORM STREQUAL "windows-arm64")
  message(FATAL_ERROR "The pinned media libraries require Windows x64.")
endif()

# 上游 GitHub release 只保留最近若干次构建，旧版本会被删除；固定单个地址
# 会随时变成 404，导致 Windows 配置阶段直接失败。候选按「地址|SHA256」给出，
# 依次尝试并逐个校验摘要；DUANJU_NATIVE_ARCHIVE_DIR 可复用预先下载的归档。
function(duanju_native_archive filename sources destination)
  set(archive "${CMAKE_BINARY_DIR}/${filename}")
  set(digests "")
  foreach(source IN LISTS sources)
    string(REPLACE "|" ";" source_parts "${source}")
    list(GET source_parts 1 digest)
    list(APPEND digests "${digest}")
  endforeach()

  if(NOT EXISTS "${archive}" AND NOT "$ENV{DUANJU_NATIVE_ARCHIVE_DIR}" STREQUAL "")
    set(prefetched "$ENV{DUANJU_NATIVE_ARCHIVE_DIR}/${filename}")
    if(EXISTS "${prefetched}")
      file(SHA256 "${prefetched}" prefetched_digest)
      list(FIND digests "${prefetched_digest}" prefetched_index)
      if(NOT prefetched_index EQUAL -1)
        file(COPY "${prefetched}" DESTINATION "${CMAKE_BINARY_DIR}")
      endif()
    endif()
  endif()

  if(EXISTS "${archive}")
    file(SHA256 "${archive}" actual_digest)
    list(FIND digests "${actual_digest}" digest_index)
    if(digest_index EQUAL -1)
      file(REMOVE "${archive}")
    endif()
  endif()

  if(NOT EXISTS "${archive}")
    set(downloaded FALSE)
    foreach(source IN LISTS sources)
      string(REPLACE "|" ";" source_parts "${source}")
      list(GET source_parts 0 url)
      list(GET source_parts 1 digest)
      message(STATUS "下载 ${filename}: ${url}")
      file(DOWNLOAD "${url}" "${archive}"
        TLS_VERIFY ON
        TIMEOUT 300
        INACTIVITY_TIMEOUT 45
        STATUS download_status
      )
      list(GET download_status 0 download_code)
      if(download_code EQUAL 0)
        file(SHA256 "${archive}" actual_digest)
        if(actual_digest STREQUAL digest)
          set(downloaded TRUE)
          break()
        endif()
        message(WARNING "${filename} 摘要不匹配：${actual_digest}")
      else()
        message(WARNING "${filename} 下载失败：${download_status}")
      endif()
      file(REMOVE "${archive}")
    endforeach()
    if(NOT downloaded)
      message(FATAL_ERROR "Failed to download ${filename}: all pinned sources failed")
    endif()
  endif()

  file(SHA256 "${archive}" selected_digest)

  set(valid FALSE)
  if(EXISTS "${destination}/.duanju-sha256")
    file(READ "${destination}/.duanju-sha256" extracted_digest)
    if(extracted_digest STREQUAL selected_digest)
      set(valid TRUE)
    endif()
  endif()
  foreach(required IN LISTS ARGN)
    if(NOT EXISTS "${destination}/${required}")
      set(valid FALSE)
    endif()
  endforeach()
  if(NOT valid)
    file(REMOVE_RECURSE "${destination}")
    file(MAKE_DIRECTORY "${destination}")
    execute_process(
      COMMAND "${CMAKE_COMMAND}" -E tar xf "${archive}"
      WORKING_DIRECTORY "${destination}"
      RESULT_VARIABLE extract_result
    )
    if(NOT extract_result EQUAL 0)
      message(FATAL_ERROR "Failed to extract ${filename}")
    endif()
    foreach(required IN LISTS ARGN)
      if(NOT EXISTS "${destination}/${required}")
        message(FATAL_ERROR "${filename} is missing ${required}")
      endif()
    endforeach()
    file(WRITE "${destination}/.duanju-sha256" "${selected_digest}")
  endif()
endfunction()

set(LIBMPV_SRC "${CMAKE_BINARY_DIR}/libmpv")
# 按发布时间由新到旧排列；每个候选都带上游 release 提供的官方 SHA256。
set(DUANJU_LIBMPV_SOURCES
  "https://github.com/shinchiro/mpv-winbuild-cmake/releases/download/20261111/mpv-dev-x86_64-20261111-git-bcde6b93cc.7z|273c079b11ea5575822f972756f4c9a71149a5c10130353abeb8586738938452"
  "https://github.com/shinchiro/mpv-winbuild-cmake/releases/download/20261110/mpv-dev-x86_64-20261110-git-a61f6c3abd.7z|f07b97a72f2394a7798d6e1d36d952bbfd63577e25cef051f716751ea5dc9688"
  "https://github.com/shinchiro/mpv-winbuild-cmake/releases/download/20261109/mpv-dev-x86_64-20261109-git-8bc468896e.7z|4e888424015f7146893d03c4d40e2aaf9474d8d47e761bcc6a97489008a7e425"
  "https://github.com/shinchiro/mpv-winbuild-cmake/releases/download/20261021/mpv-dev-x86_64-20261021-git-e471496c56.7z|efc719eff3dc5954b4a5efc800632808bcec01cc9034940dd14e5eb7fe9ea633"
)
duanju_native_archive(
  "mpv-dev-x86_64-20261111-git-bcde6b93cc.7z"
  "${DUANJU_LIBMPV_SOURCES}"
  "${LIBMPV_SRC}"
  "libmpv-2.dll" "libmpv.dll.a" "include/mpv/client.h" "include/mpv/render.h"
)
file(COPY "${LIBMPV_SRC}/include/mpv/" DESTINATION "${LIBMPV_SRC}/include")

set(ANGLE_SRC "${CMAKE_BINARY_DIR}/ANGLE")
duanju_native_archive(
  "ANGLE-v1.0.1.7z"
  "https://github.com/alexmercerind/flutter-windows-ANGLE-OpenGL-ES/releases/download/v1.0.1/ANGLE.7z|cc5911bb15d596fd5a2b362613ad35b7093b427117269a7359054a65746a5f9a"
  "${ANGLE_SRC}"
  "include/EGL/egl.h" "lib/libEGL.dll.lib" "lib/libGLESv2.dll.lib"
  "d3dcompiler_47.dll" "libc++.dll" "libEGL.dll" "libGLESv2.dll"
  "vk_swiftshader.dll" "vulkan-1.dll" "zlib.dll"
)

set(PLUGIN_NAME "media_kit_libs_windows_video_plugin")
add_library(${PLUGIN_NAME} SHARED
  "include/media_kit_libs_windows_video/media_kit_libs_windows_video_plugin_c_api.h"
  "media_kit_libs_windows_video_plugin_c_api.cpp"
)
apply_standard_settings(${PLUGIN_NAME})
set_target_properties(${PLUGIN_NAME} PROPERTIES CXX_VISIBILITY_PRESET hidden)
target_compile_definitions(${PLUGIN_NAME} PRIVATE FLUTTER_PLUGIN_IMPL)
target_include_directories(${PLUGIN_NAME} INTERFACE "${CMAKE_CURRENT_SOURCE_DIR}/include")
target_link_libraries(${PLUGIN_NAME} PRIVATE flutter flutter_wrapper_plugin)

set(media_kit_libs_windows_video_bundled_libraries
  "${LIBMPV_SRC}/libmpv-2.dll"
  "${ANGLE_SRC}/d3dcompiler_47.dll"
  "${ANGLE_SRC}/libc++.dll"
  "${ANGLE_SRC}/libEGL.dll"
  "${ANGLE_SRC}/libGLESv2.dll"
  "${ANGLE_SRC}/vk_swiftshader.dll"
  "${ANGLE_SRC}/vulkan-1.dll"
  "${ANGLE_SRC}/zlib.dll"
  PARENT_SCOPE
)
