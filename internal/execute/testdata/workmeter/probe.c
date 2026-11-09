#define _GNU_SOURCE
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

int main(void) {
    char mode[16] = {0};
    if (scanf("%15s", mode) != 1) return 2;
    if (!strcmp(mode, "normal")) {
        unsigned long long n = 0, sum = 0;
        if (scanf("%llu", &n) != 1 || n > 10000000) return 3;
        for (volatile unsigned long long i = 0; i < n; ++i) sum += i;
        printf("%llu\n", sum);
        return 0;
    }
    if (!strcmp(mode, "fork")) {
        pid_t child = fork();
        if (child == 0) _exit(0);
        if (child > 0) { waitpid(child, NULL, 0); puts("unexpected-fork"); return 4; }
        if (errno != EPERM) return 5;
        puts("blocked"); return 0;
    }
    if (!strcmp(mode, "network")) {
        int fd = socket(AF_INET, SOCK_STREAM, 0);
        if (fd >= 0) { close(fd); puts("unexpected-network"); return 6; }
        if (errno != EPERM) return 7;
        puts("blocked"); return 0;
    }
    if (!strcmp(mode, "burn")) {
        volatile unsigned long long x = 0;
        for (;;) ++x;
    }
    return 8;
}
