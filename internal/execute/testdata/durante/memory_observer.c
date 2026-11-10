/* Trusted measurement-policy counterexample, not a judge algorithm.
 * Identical code, input and output do not prove identical allocations when
 * instrumentation changes an observable execution environment. */
#define _GNU_SOURCE
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

int main(void)
{
    FILE *maps = fopen("/proc/self/maps", "r");
    if (!maps) return 2;
    char line[2048];
    int instrumented = 0;
    while (fgets(line, sizeof(line), maps))
        if (strstr(line, "libdynamorio.so")) instrumented = 1;
    if (ferror(maps)) { fclose(maps); return 2; }
    if (fclose(maps)) return 2;
    size_t bytes = (size_t)(instrumented ? 64 : 16) << 20;
    long page_size = sysconf(_SC_PAGESIZE);
    if (page_size <= 0) return 2;
    unsigned char *allocation = mmap(NULL, bytes, PROT_READ | PROT_WRITE,
                                    MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    if (allocation == MAP_FAILED) return 3;
    for (size_t offset = 0; offset < bytes; offset += (size_t)page_size)
        ((volatile unsigned char *)allocation)[offset] = 1;
    struct timespec hold = {0, 20000000};
    if (nanosleep(&hold, NULL) || munmap(allocation, bytes)) return 4;
    puts("same answer");
    return 0;
}
