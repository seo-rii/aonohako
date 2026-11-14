#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static void burn(void) {
    volatile unsigned long long value = 0;
    for (;;) ++value;
}

static void wait_forever(void) {
    for (;;) sleep(1);
}

static int contestant(void) {
    char mode[32];
    if (scanf("%31s", mode) != 1) return 2;
    if (!strcmp(mode, "burn") || !strcmp(mode, "wait")) {
        puts("ready");
        if (!strcmp(mode, "burn")) burn();
        wait_forever();
    }
    puts("42");
    return 0;
}

static int interactor(const char *input_path) {
    char mode[32];
    FILE *input = fopen(input_path, "r");
    if (!input) return 3;
    int scanned = fscanf(input, "%31s", mode);
    fclose(input);
    if (scanned != 1) return 3;
    puts(mode);

    char answer[32];
    if (!fgets(answer, sizeof(answer), stdin)) return 3;
    if (!strcmp(mode, "burn") || !strcmp(mode, "wait")) {
        if (strcmp(answer, "ready\n")) return 3;
        wait_forever();
    }
    if (strcmp(answer, "42\n")) return 3;
    // The relay must propagate contestant stdout closure as peer EOF.
    if (fgetc(stdin) != EOF || ferror(stdin)) return 3;
    // Allow the exited contestant's runtime to flush its observation before
    // a wrong verdict or a final reply causes peer cancellation/relay EPIPE.
    usleep(200000);
    if (!strcmp(mode, "interactor_burn")) burn();
    if (!strcmp(mode, "final_accept")) puts("correct");
    if (!strcmp(mode, "final_wrong")) puts("wrong");
    return !strcmp(mode, "wrong") || !strcmp(mode, "final_wrong") ? 1 : 0;
}

int main(int argc, char **argv) {
    if (setvbuf(stdout, NULL, _IONBF, 0)) return 3;
    if (argc == 1) return contestant();
    // The interactive runner supplies input, output and answer paths.
    if (argc == 4) return interactor(argv[1]);
    return 3;
}
