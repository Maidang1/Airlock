#ifndef CAGE_MACNATIVE_H
#define CAGE_MACNATIVE_H

#include <stddef.h>

int cage_parse_plist_json(const void *data, size_t len, char **out_json, char **out_err);

int cage_biometric_available(void);
const char *cage_keychain_status(const char *service, const char *account, const char *expected_id, int is_biometric);
int cage_keychain_store(const char *service, const char *account, const char *label, const void *data, size_t len, const char *id_str, int is_biometric, char **out_err);
int cage_keychain_read(const char *service, const char *account, const char *reason, int interactive, void **out_data, size_t *out_len, char **out_err);
int cage_keychain_remove(const char *service, const char *account, char **out_err);

long cage_pasteboard_change_count(void);
char *cage_pasteboard_read_text(void);
int cage_pasteboard_write_text(const char *text);
int cage_pasteboard_write_concealed_text(const char *text, int clear_seconds);
int cage_pasteboard_read_files_json(char **out_json);
int cage_pasteboard_write_files_json(const char *paths_json);
int cage_pasteboard_read_png(void **out_data, size_t *out_len, char **out_err);
int cage_pasteboard_write_png(const void *data, size_t len, char **out_err);

int cage_signing_info(const char *path, int is_bundle, const char *bundle_id, const char *expected_team, char **out_team, char **out_err);
int cage_replace_item_at(const char *target_path, const char *candidate_path, int request_auth, char **out_err);
int cage_launch_app_with_env(const char *app_path, const char *env_json, int *out_pid, char **out_err);
int cage_running_app_info(int pid, char **out_bundle_path, int *out_terminated);

#endif
