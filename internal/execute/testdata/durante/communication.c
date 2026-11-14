#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define PAYLOAD_BYTES (256U * 1024U)
static unsigned char payload[PAYLOAD_BYTES];

static int fail(int code) {
  fprintf(stderr, "durante communication fixture: code=%d errno=%d\n", code, errno);
  return code;
}

static int read_word(const char *path, char value[32]) {
  FILE *file = fopen(path, "r");
  if (!file) return 0;
  int ok = fscanf(file, "%31s", value) == 1;
  fclose(file);
  return ok;
}

static int write_all(int fd, const void *data, size_t size) {
  const unsigned char *bytes = data;
  while (size > 0) {
    ssize_t n = write(fd, bytes, size);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return 0;
    bytes += (size_t)n;
    size -= (size_t)n;
  }
  return 1;
}

static int read_all(int fd, void *data, size_t size) {
  unsigned char *bytes = data;
  while (size > 0) {
    ssize_t n = read(fd, bytes, size);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return 0;
    bytes += (size_t)n;
    size -= (size_t)n;
  }
  return 1;
}

static int read_eof(int fd) {
  unsigned char byte;
  ssize_t n;
  do { n = read(fd, &byte, 1); } while (n < 0 && errno == EINTR);
  return n == 0;
}

static unsigned char request_byte(size_t offset, unsigned int seat) {
  return (unsigned char)((offset * 37U + offset / 251U + seat * 17U) % 251U);
}

static int pipe_fd(const char *path) {
  int fd = -1;
  char trailing;
  if (sscanf(path, "/proc/self/fd/%d%c", &fd, &trailing) != 1 || fd < 6) return -1;
  return fd;
}

static int participant(const char *seat_arg, const char *mode) {
  unsigned int seat = (unsigned int)strtoul(seat_arg, NULL, 10);
  if (seat > 1 || getuid() != 65000U - seat) return fail(10);
  char marker[32];
  if (!read_word("marker.dat", marker)) return fail(11);
  const char *expected_marker = seat == 0 ? "seat0" : "seat1";
  if (strcmp(marker, "shared") && strcmp(marker, expected_marker)) return fail(12);
  if (!strcmp(mode, "exit")) return 17;
  if (!strcmp(mode, "wait") || !strcmp(mode, "burn")) {
    int pid = (int)getpid();
    if (!write_all(STDOUT_FILENO, &pid, sizeof(pid))) return fail(13);
    if (!strcmp(mode, "burn")) {
      volatile uint64_t count = 0;
      for (;;) count++;
    }
    for (;;) pause();
  }
  if (strcmp(mode, "stream")) return fail(14);
  if (!read_all(STDIN_FILENO, payload, sizeof(payload))) return fail(15);
  for (size_t i = 0; i < sizeof(payload); ++i) {
    if (payload[i] != request_byte(i, seat)) return fail(16);
    payload[i] ^= 0xa5U;
  }
  if (!read_eof(STDIN_FILENO)) return fail(18);
  if (!write_all(STDOUT_FILENO, payload, sizeof(payload))) return fail(19);
  return 0;
}

static int write_result(int fd, int version) {
  const char *result = version == 2
    ? "{\"version\":1,\"wins\":[7,13],\"games\":20}"
    : "{\"verdict\":\"accepted\",\"score\":1,\"message\":\"durante streams and EOF\"}";
  if (!write_all(fd, result, strlen(result)) || close(fd) != 0) return fail(30);
  return 0;
}

static int manager(int argc, char **argv, const char *mode) {
  if ((argc != 9 && argc != 10) || strcmp(argv[4], "2") || getuid() != 65531U) return fail(31);
  int version = argc == 10 ? 2 : 1;
  if (version == 2 && (strlen(argv[9]) != 64 || argv[9][0] != '3')) return fail(32);
  int result_fd = pipe_fd(argv[3]);
  if (result_fd < 0) return fail(33);
  if (!strcmp(mode, "error")) return 29;
  if (!strcmp(mode, "invalid")) {
    if (!write_all(result_fd, "{invalid", 8) || close(result_fd) != 0) return fail(34);
    return 0;
  }
  if (!strcmp(mode, "forfeit")) {
    int fd = pipe_fd(argv[5]);
    const char *result = "{\"version\":1,\"wins\":[0,20],\"games\":20}";
    if (version != 2 || fd < 0 || !read_eof(fd)) return fail(46);
    if (!write_all(result_fd, result, strlen(result)) || close(result_fd) != 0) return fail(47);
    return 0;
  }
  if (!strcmp(mode, "wait") || !strcmp(mode, "early")) {
    int pids[2];
    for (int seat = 0; seat < 2; ++seat) {
      int fd = pipe_fd(argv[5 + 2 * seat]);
      if (fd < 0 || !read_all(fd, &pids[seat], sizeof(pids[seat]))) return fail(35);
    }
    if (!strcmp(mode, "early")) return write_result(result_fd, version);
    FILE *input = fopen(argv[1], "r");
    char ready_path[4096];
    if (!input) return fail(36);
    int has_path = fscanf(input, "%4095s", ready_path) == 1;
    fclose(input);
    if (has_path) {
      FILE *ready = fopen(ready_path, "w");
      if (!ready) return fail(37);
      if (fprintf(ready, "%d %d %d\n", pids[0], pids[1], (int)getpid()) < 0 || fclose(ready) != 0) return fail(38);
    }
    for (;;) pause();
  }
  if (strcmp(mode, "stream")) return fail(39);
  for (unsigned int seat = 0; seat < 2; ++seat) {
    int read_fd = pipe_fd(argv[5 + 2 * seat]);
    int write_fd = pipe_fd(argv[6 + 2 * seat]);
    if (read_fd < 0 || write_fd < 0) return fail(40);
    int capacity = fcntl(write_fd, F_GETPIPE_SZ);
    if (capacity <= 0 || (size_t)capacity >= sizeof(payload)) return fail(41);
    for (size_t i = 0; i < sizeof(payload); ++i) payload[i] = request_byte(i, seat);
    if (!write_all(write_fd, payload, sizeof(payload)) || close(write_fd) != 0) return fail(42);
    if (!read_all(read_fd, payload, sizeof(payload))) return fail(43);
    for (size_t i = 0; i < sizeof(payload); ++i) {
      if (payload[i] != (unsigned char)(request_byte(i, seat) ^ 0xa5U)) return fail(44);
    }
    if (!read_eof(read_fd) || close(read_fd) != 0) return fail(45);
  }
  return write_result(result_fd, version);
}

int main(int argc, char **argv) {
  signal(SIGPIPE, SIG_IGN);
  char mode[32];
  if (!read_word("mode.dat", mode)) return fail(1);
  if (argc == 2) return participant(argv[1], mode);
  return manager(argc, argv, mode);
}
