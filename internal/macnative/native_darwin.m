//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <LocalAuthentication/LocalAuthentication.h>
#import <Security/Security.h>
#include <stdlib.h>
#include <string.h>
#include "native_darwin.h"

static char *dup_nsstring(NSString *s) {
    if (!s) return NULL;
    const char *utf8 = [s UTF8String];
    return utf8 ? strdup(utf8) : NULL;
}

static void set_err(char **out_err, NSString *msg) {
    if (out_err && msg) {
        *out_err = dup_nsstring(msg);
    }
}

static id json_safe_plist(id obj) {
    if ([obj isKindOfClass:[NSString class]] || [obj isKindOfClass:[NSNumber class]]) {
        return obj;
    }
    if ([obj isKindOfClass:[NSDate class]]) {
        NSISO8601DateFormatter *fmt = [[NSISO8601DateFormatter alloc] init];
        return [fmt stringFromDate:obj];
    }
    if ([obj isKindOfClass:[NSData class]]) {
        return [(NSData *)obj base64EncodedStringWithOptions:0];
    }
    if ([obj isKindOfClass:[NSArray class]]) {
        NSMutableArray *arr = [NSMutableArray array];
        for (id item in (NSArray *)obj) {
            id safe = json_safe_plist(item);
            if (safe) [arr addObject:safe];
        }
        return arr;
    }
    if ([obj isKindOfClass:[NSDictionary class]]) {
        NSMutableDictionary *dict = [NSMutableDictionary dictionary];
        [(NSDictionary *)obj enumerateKeysAndObjectsUsingBlock:^(id key, id val, BOOL *stop) {
            if ([key isKindOfClass:[NSString class]]) {
                id safe = json_safe_plist(val);
                if (safe) dict[key] = safe;
            }
        }];
        return dict;
    }
    return [obj description];
}

int cage_parse_plist_json(const void *data, size_t len, char **out_json, char **out_err) {
    @autoreleasepool {
        if (!data || len == 0) {
            set_err(out_err, @"empty plist data");
            return -1;
        }
        NSData *nsdata = [NSData dataWithBytes:data length:len];
        NSError *err = nil;
        id obj = [NSPropertyListSerialization propertyListWithData:nsdata options:NSPropertyListImmutable format:NULL error:&err];
        if (!obj || ![obj isKindOfClass:[NSDictionary class]]) {
            set_err(out_err, err ? err.localizedDescription : @"invalid plist dictionary");
            return -1;
        }
        id safe = json_safe_plist(obj);
        NSData *json = [NSJSONSerialization dataWithJSONObject:safe options:0 error:&err];
        if (!json) {
            set_err(out_err, err ? err.localizedDescription : @"plist json conversion failed");
            return -1;
        }
        NSString *str = [[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding];
        *out_json = dup_nsstring(str);
        return 0;
    }
}

int cage_biometric_available(void) {
    @autoreleasepool {
        LAContext *ctx = [[LAContext alloc] init];
        NSError *err = nil;
        return [ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:&err] ? 1 : 0;
    }
}

static NSMutableDictionary *keychain_query(const char *service, const char *account, int is_biometric) {
    NSMutableDictionary *q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    if (is_biometric) {
        q[(__bridge id)kSecUseDataProtectionKeychain] = @YES;
        q[(__bridge id)kSecAttrSynchronizable] = @NO;
    }
    return q;
}

const char *cage_keychain_status(const char *service, const char *account, const char *expected_id, int is_biometric) {
    @autoreleasepool {
        if (!expected_id || expected_id[0] == '\0') {
            return "disabled";
        }
        NSMutableDictionary *q = keychain_query(service, account, is_biometric);
        q[(__bridge id)kSecReturnAttributes] = @YES;
        q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
        LAContext *ctx = [[LAContext alloc] init];
        ctx.interactionNotAllowed = YES;
        q[(__bridge id)kSecUseAuthenticationContext] = ctx;

        CFTypeRef result = NULL;
        OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)q, &result);
        NSDictionary *attrs = CFBridgingRelease(result);
        if (status == errSecItemNotFound) return "missing";
        if (is_biometric && status == errSecInteractionNotAllowed) return "authentication_required";
        if (status != errSecSuccess || !attrs) return "unavailable";

        NSData *storedID = attrs[(__bridge id)kSecAttrGeneric];
        if (storedID) {
            NSData *want = [[NSString stringWithUTF8String:expected_id] dataUsingEncoding:NSUTF8StringEncoding];
            if (![storedID isEqualToData:want]) return "missing";
        }
        return "available";
    }
}

int cage_keychain_remove(const char *service, const char *account, char **out_err) {
    @autoreleasepool {
        // Delete from both default and data protection keychains if needed based on caller intent,
        // first try standard query then biometric data protection query if requested separately.
        NSMutableDictionary *q1 = keychain_query(service, account, 0);
        OSStatus s1 = SecItemDelete((__bridge CFDictionaryRef)q1);
        NSMutableDictionary *q2 = keychain_query(service, account, 1);
        OSStatus s2 = SecItemDelete((__bridge CFDictionaryRef)q2);
        if ((s1 == errSecSuccess || s1 == errSecItemNotFound) &&
            (s2 == errSecSuccess || s2 == errSecItemNotFound || s2 == errSecMissingEntitlement)) {
            return 0;
        }
        OSStatus fail = (s1 != errSecSuccess && s1 != errSecItemNotFound) ? s1 : s2;
        set_err(out_err, [NSString stringWithFormat:@"无法移除本机钥匙串记录（%d），请重试", (int)fail]);
        return -1;
    }
}

int cage_keychain_store(const char *service, const char *account, const char *label, const void *data, size_t len, const char *id_str, int is_biometric, char **out_err) {
    @autoreleasepool {
        NSMutableDictionary *delQ = keychain_query(service, account, is_biometric);
        OSStatus delStatus = SecItemDelete((__bridge CFDictionaryRef)delQ);
        if (delStatus != errSecSuccess && delStatus != errSecItemNotFound) {
            if (delStatus == errSecMissingEntitlement) {
                set_err(out_err, @"当前开发构建缺少钥匙串访问描述文件，指纹解锁仅支持正式签名版；密码与记住登录仍可使用");
            } else {
                set_err(out_err, @"无法移除本机钥匙串记录，请重试");
            }
            return -1;
        }

        NSMutableDictionary *item = keychain_query(service, account, is_biometric);
        item[(__bridge id)kSecValueData] = [NSData dataWithBytes:data length:len];
        if (id_str && id_str[0] != '\0') {
            item[(__bridge id)kSecAttrGeneric] = [[NSString stringWithUTF8String:id_str] dataUsingEncoding:NSUTF8StringEncoding];
        }
        if (label && label[0] != '\0') {
            item[(__bridge id)kSecAttrLabel] = [NSString stringWithUTF8String:label];
        }
        if (is_biometric) {
            CFErrorRef cfErr = NULL;
            SecAccessControlRef access = SecAccessControlCreateWithFlags(
                NULL,
                kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
                kSecAccessControlBiometryCurrentSet,
                &cfErr);
            if (!access) {
                if (cfErr) CFRelease(cfErr);
                set_err(out_err, @"这台 Mac 无法创建指纹保护的凭据");
                return -1;
            }
            item[(__bridge id)kSecAttrAccessControl] = (__bridge_transfer id)access;
        }

        OSStatus status = SecItemAdd((__bridge CFDictionaryRef)item, NULL);
        if (status != errSecSuccess) {
            if (status == errSecMissingEntitlement) {
                set_err(out_err, @"当前构建未包含指纹钥匙串描述文件，请使用密码或记住登录");
            } else {
                set_err(out_err, [NSString stringWithFormat:@"macOS 未能保存快捷登录凭据（%d），密码登录仍然可用", (int)status]);
            }
            return -1;
        }
        return 0;
    }
}

int cage_keychain_read(const char *service, const char *account, const char *reason, int interactive, void **out_data, size_t *out_len, char **out_err) {
    @autoreleasepool {
        int is_biometric = interactive ? 1 : 0;
        NSMutableDictionary *item = keychain_query(service, account, is_biometric);
        item[(__bridge id)kSecReturnData] = @YES;
        item[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;

        LAContext *ctx = [[LAContext alloc] init];
        if (reason && reason[0] != '\0') {
            ctx.localizedReason = [NSString stringWithUTF8String:reason];
        }
        ctx.localizedFallbackTitle = @"";
        if (!interactive) {
            ctx.interactionNotAllowed = YES;
        }
        item[(__bridge id)kSecUseAuthenticationContext] = ctx;

        CFTypeRef result = NULL;
        OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)item, &result);
        NSData *data = CFBridgingRelease(result);
        if (status == errSecUserCanceled) {
            set_err(out_err, @"已取消指纹验证，可继续使用密码");
            return -1;
        }
        if (status != errSecSuccess || !data) {
            set_err(out_err, @"快捷登录不可用，请使用密码解锁后重新开启");
            return -1;
        }
        void *buf = malloc(data.length > 0 ? data.length : 1);
        if (!buf) {
            set_err(out_err, @"memory allocation failed");
            return -1;
        }
        if (data.length > 0) memcpy(buf, data.bytes, data.length);
        *out_data = buf;
        *out_len = data.length;
        return 0;
    }
}

long cage_pasteboard_change_count(void) {
    @autoreleasepool {
        return (long)[[NSPasteboard generalPasteboard] changeCount];
    }
}

char *cage_pasteboard_read_text(void) {
    @autoreleasepool {
        NSString *str = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
        return str ? dup_nsstring(str) : dup_nsstring(@"");
    }
}

int cage_pasteboard_write_text(const char *text) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        NSString *str = [NSString stringWithUTF8String:(text ? text : "")];
        return [pb setString:str forType:NSPasteboardTypeString] ? 0 : -1;
    }
}

int cage_pasteboard_write_concealed_text(const char *text, int clear_seconds) {
    @autoreleasepool {
        NSString *str = [NSString stringWithUTF8String:(text ? text : "")];
        NSPasteboardItem *item = [[NSPasteboardItem alloc] init];
        [item setString:str forType:NSPasteboardTypeString];
        [item setData:[NSData data] forType:@"org.nspasteboard.ConcealedType"];
        [item setData:[NSData data] forType:@"org.nspasteboard.TransientType"];
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        if (![pb writeObjects:@[item]]) {
            return -1;
        }
        if (clear_seconds > 0) {
            NSInteger change = [pb changeCount];
            dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)clear_seconds * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
                if ([pb changeCount] == change) {
                    [pb clearContents];
                }
            });
        }
        return 0;
    }
}

int cage_pasteboard_read_files_json(char **out_json) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSArray *urls = [pb readObjectsForClasses:@[[NSURL class]] options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
        NSMutableArray *paths = [NSMutableArray array];
        for (NSURL *u in urls) {
            if ([u isFileURL] && u.path.length > 0) {
                [paths addObject:u.path];
            }
        }
        NSError *err = nil;
        NSData *json = [NSJSONSerialization dataWithJSONObject:paths options:0 error:&err];
        if (!json) return -1;
        NSString *str = [[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding];
        *out_json = dup_nsstring(str);
        return 0;
    }
}

int cage_pasteboard_write_files_json(const char *paths_json) {
    @autoreleasepool {
        if (!paths_json) return -1;
        NSData *data = [[NSString stringWithUTF8String:paths_json] dataUsingEncoding:NSUTF8StringEncoding];
        NSArray *paths = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
        if (![paths isKindOfClass:[NSArray class]]) return -1;
        NSMutableArray *urls = [NSMutableArray arrayWithCapacity:paths.count];
        for (id p in paths) {
            if ([p isKindOfClass:[NSString class]]) {
                [urls addObject:[NSURL fileURLWithPath:p]];
            }
        }
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        return [pb writeObjects:urls] ? 0 : -1;
    }
}

int cage_pasteboard_read_png(void **out_data, size_t *out_len, char **out_err) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSImage *img = [[NSImage alloc] initWithPasteboard:pb];
        if (!img) return 1; // no image
        NSData *tiff = [img TIFFRepresentation];
        if (!tiff) return 1;
        NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
        if (!rep) return 1;
        if (rep.pixelsWide <= 0 || rep.pixelsHigh <= 0 || rep.pixelsWide > 40000000 / rep.pixelsHigh) {
            set_err(out_err, @"图片不能超过 20 MB 或 4000 万像素");
            return -1;
        }
        NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
        if (!png || png.length > 20 * 1024 * 1024) {
            set_err(out_err, @"图片不能超过 20 MB 或 4000 万像素");
            return -1;
        }
        void *buf = malloc(png.length > 0 ? png.length : 1);
        if (!buf) return -1;
        memcpy(buf, png.bytes, png.length);
        *out_data = buf;
        *out_len = png.length;
        return 0;
    }
}

int cage_pasteboard_write_png(const void *data, size_t len, char **out_err) {
    @autoreleasepool {
        if (!data || len == 0 || len > 20 * 1024 * 1024) {
            set_err(out_err, @"剪贴板图片内容格式或大小无效");
            return -1;
        }
        NSData *nsdata = [NSData dataWithBytes:data length:len];
        NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:nsdata];
        if (!rep || rep.pixelsWide <= 0 || rep.pixelsHigh <= 0 || rep.pixelsWide > 40000000 / rep.pixelsHigh) {
            set_err(out_err, @"剪贴板图片内容格式或大小无效");
            return -1;
        }
        NSImage *img = [[NSImage alloc] initWithSize:rep.size];
        [img addRepresentation:rep];
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        if (![pb writeObjects:@[img]]) {
            set_err(out_err, @"剪贴板写入失败");
            return -1;
        }
        return 0;
    }
}

int cage_signing_info(const char *path, int is_bundle, const char *bundle_id, const char *expected_team, char **out_team, char **out_err) {
    @autoreleasepool {
        NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
        SecStaticCodeRef code = NULL;
        if (SecStaticCodeCreateWithPath((__bridge CFURLRef)url, kSecCSDefaultFlags, &code) != errSecSuccess || !code) {
            set_err(out_err, @"无法读取应用签名");
            return -1;
        }
        NSString *ident = (is_bundle && bundle_id && bundle_id[0]) ? [NSString stringWithFormat:@" and identifier \"%s\"", bundle_id] : @"";
        NSString *team = (expected_team && expected_team[0]) ? [NSString stringWithFormat:@" and certificate leaf[subject.OU] = \"%s\"", expected_team] : @"";
        NSString *expr = [NSString stringWithFormat:@"anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists%@%@", ident, team];

        SecRequirementRef req = NULL;
        if (SecRequirementCreateWithString((__bridge CFStringRef)expr, kSecCSDefaultFlags, &req) != errSecSuccess || !req) {
            CFRelease(code);
            set_err(out_err, @"无法建立发布者签名要求");
            return -1;
        }
        SecCSFlags flags = kSecCSStrictValidate | kSecCSCheckAllArchitectures | kSecCSCheckNestedCode;
        OSStatus valid = SecStaticCodeCheckValidity(code, flags, req);
        CFRelease(req);
        if (valid != errSecSuccess) {
            CFRelease(code);
            set_err(out_err, @"更新应用的 Apple 签名无效或发布者不匹配");
            return -1;
        }
        CFDictionaryRef infoRef = NULL;
        OSStatus infoStatus = SecCodeCopySigningInformation(code, kSecCSSigningInformation, &infoRef);
        CFRelease(code);
        NSDictionary *info = CFBridgingRelease(infoRef);
        if (infoStatus != errSecSuccess || !info) {
            set_err(out_err, @"当前开发构建没有可验证的发布者，无法自动安装更新");
            return -1;
        }
        NSString *actual = info[(__bridge id)kSecCodeInfoTeamIdentifier];
        NSRegularExpression *re = [NSRegularExpression regularExpressionWithPattern:@"^[A-Z0-9]{10}$" options:0 error:nil];
        if (![actual isKindOfClass:[NSString class]] ||
            [re numberOfMatchesInString:actual options:0 range:NSMakeRange(0, actual.length)] == 0) {
            set_err(out_err, @"当前开发构建没有可验证的发布者，无法自动安装更新");
            return -1;
        }
        if (expected_team && expected_team[0] && ![actual isEqualToString:[NSString stringWithUTF8String:expected_team]]) {
            set_err(out_err, @"更新应用的发布者不匹配");
            return -1;
        }
        *out_team = dup_nsstring(actual);
        return 0;
    }
}

int cage_replace_item_at(const char *target_path, const char *candidate_path, int request_auth, char **out_err) {
    @autoreleasepool {
        NSURL *targetURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:target_path]];
        NSURL *candidateURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:candidate_path]];
        NSFileManager *fm = [NSFileManager defaultManager];

        if (request_auth) {
            NSApplicationLoad();
            dispatch_semaphore_t sem = dispatch_semaphore_create(0);
            __block NSWorkspaceAuthorization *auth = nil;
            __block BOOL done = NO;
            [[NSWorkspace sharedWorkspace] requestAuthorizationOfType:NSWorkspaceAuthorizationTypeReplaceFile completionHandler:^(NSWorkspaceAuthorization * _Nullable a, NSError * _Nullable error) {
                auth = a;
                done = YES;
                dispatch_semaphore_signal(sem);
            }];
            if ([NSThread isMainThread]) {
                while (!done) {
                    @autoreleasepool {
                        [[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
                    }
                }
            } else {
                dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
            }
            if (!auth) {
                set_err(out_err, @"未获得安装权限，原应用已保留");
                return -1;
            }
            fm = [NSFileManager fileManagerWithAuthorization:auth];
        }

        NSError *err = nil;
        BOOL ok = [fm replaceItemAtURL:targetURL withItemAtURL:candidateURL backupItemName:nil options:NSFileManagerItemReplacementUsingNewMetadataOnly resultingItemURL:nil error:&err];
        if (!ok) {
            set_err(out_err, err ? err.localizedDescription : @"应用替换失败");
            return -1;
        }
        return 0;
    }
}

int cage_launch_app_with_env(const char *app_path, const char *env_json, int *out_pid, char **out_err) {
    @autoreleasepool {
        NSURL *appURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:app_path]];
        NSWorkspaceOpenConfiguration *cfg = [NSWorkspaceOpenConfiguration configuration];
        cfg.activates = YES;
        cfg.createsNewApplicationInstance = YES;
        if (env_json && env_json[0]) {
            NSData *data = [[NSString stringWithUTF8String:env_json] dataUsingEncoding:NSUTF8StringEncoding];
            NSDictionary *env = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
            if ([env isKindOfClass:[NSDictionary class]]) {
                cfg.environment = env;
            }
        }
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        __block pid_t pid = 0;
        __block NSError *openErr = nil;
        [[NSWorkspace sharedWorkspace] openApplicationAtURL:appURL configuration:cfg completionHandler:^(NSRunningApplication * _Nullable app, NSError * _Nullable error) {
            if (app) pid = app.processIdentifier;
            openErr = error;
            dispatch_semaphore_signal(sem);
        }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
        if (openErr || pid <= 0) {
            set_err(out_err, openErr ? openErr.localizedDescription : @"无法启动应用程序");
            return -1;
        }
        if (out_pid) *out_pid = (int)pid;
        return 0;
    }
}

int cage_running_app_info(int pid, char **out_bundle_path, int *out_terminated) {
    @autoreleasepool {
        NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
        if (!app) {
            if (out_terminated) *out_terminated = 1;
            return -1;
        }
        if (out_terminated) *out_terminated = app.terminated ? 1 : 0;
        if (out_bundle_path && app.bundleURL) {
            *out_bundle_path = dup_nsstring([app.bundleURL URLByResolvingSymlinksInPath].path);
        }
        return 0;
    }
}
