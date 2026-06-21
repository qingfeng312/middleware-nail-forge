#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "../include/logger.h"

static int failures = 0;

#define EXPECT_TRUE(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s\n", message); \
        failures++; \
    } \
} while (0)

static void clear_logger_env(void)
{
    unsetenv("LOG_FILE");
    unsetenv("LOG_LEVEL");
    unsetenv("LOG_MODULE");
    unsetenv("LOG_SOURCE_INFO");
    unsetenv("LOG_NO_TIMESTAMPS");
}

static int file_contains(const char *path, const char *needle)
{
    FILE *fp = fopen(path, "r");
    if (fp == NULL) {
        return 0;
    }

    char buffer[4096];
    size_t nread = fread(buffer, 1, sizeof(buffer) - 1, fp);
    fclose(fp);
    buffer[nread] = '\0';
    return strstr(buffer, needle) != NULL;
}

static void test_open_failure_falls_back_to_stderr(void)
{
    char dir_template[] = "/tmp/frailbox-log-open-XXXXXX";
    char *dir = mkdtemp(dir_template);
    EXPECT_TRUE(dir != NULL, "mkdtemp should create a temporary directory");
    if (dir == NULL) {
        return;
    }

    char bad_path[512];
    snprintf(bad_path, sizeof(bad_path), "%s/missing/log.txt", dir);

    clear_logger_env();
    setenv("LOG_FILE", bad_path, 1);
    setenv("LOG_LEVEL", "debug", 1);

    EXPECT_TRUE(log_init() == 0, "log_init should succeed with stderr fallback");
    LOG_ERROR("open fallback marker");
    log_shutdown();

    rmdir(dir);
    clear_logger_env();
}

static void test_regular_file_logging_still_works(void)
{
    char path_template[] = "/tmp/frailbox-log-file-XXXXXX";
    int fd = mkstemp(path_template);
    EXPECT_TRUE(fd >= 0, "mkstemp should create a log file");
    if (fd < 0) {
        return;
    }
    close(fd);

    clear_logger_env();
    setenv("LOG_FILE", path_template, 1);
    setenv("LOG_LEVEL", "debug", 1);

    EXPECT_TRUE(log_init() == 0, "log_init should open a writable file");
    LOG_WARN("regular file marker");
    log_shutdown();

    EXPECT_TRUE(file_contains(path_template, "regular file marker"),
                "configured log file should contain the emitted message");

    unlink(path_template);
    clear_logger_env();
}

static void test_write_failure_does_not_abort(void)
{
    if (access("/dev/full", W_OK) != 0) {
        fprintf(stderr, "SKIP: /dev/full is unavailable on this platform\n");
        return;
    }

    clear_logger_env();
    setenv("LOG_FILE", "/dev/full", 1);
    setenv("LOG_LEVEL", "debug", 1);

    EXPECT_TRUE(log_init() == 0, "log_init should open /dev/full when available");
    LOG_ERROR("write failure marker");
    LOG_ERROR("post-fallback marker");
    log_shutdown();

    clear_logger_env();
}

int main(void)
{
    test_open_failure_falls_back_to_stderr();
    test_regular_file_logging_still_works();
    test_write_failure_does_not_abort();

    if (failures != 0) {
        fprintf(stderr, "%d logger error-handling test(s) failed\n", failures);
        return 1;
    }

    printf("logger error-handling tests passed\n");
    return 0;
}
