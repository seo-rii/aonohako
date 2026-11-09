/* Temporary experiment launcher. With no HM_SEED the upstream entrypoint is
 * unchanged. HM_SEED=random and decimal seeds use the same PyConfig path;
 * -I remains enabled and PYTHONHASHSEED remains ignored in the experiment.
 * This is not an operational policy for untrusted production submissions.
 */
#include "Python.h"
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef MS_WINDOWS
int wmain(int argc, wchar_t **argv)
{
    return Py_Main(argc, argv);
}
#else
int main(int argc, char **argv)
{
    const char *setting = getenv("HM_SEED");
    if (setting == NULL) {
        return Py_BytesMain(argc, argv);
    }

    int fixed = strcmp(setting, "random") != 0;
    unsigned long seed = 0;
    if (fixed) {
        const unsigned char *cursor = (const unsigned char *)setting;
        if (*cursor == '\0') {
            fputs("HM_SEED must be random or a decimal uint32\n", stderr);
            return 2;
        }
        for (; *cursor; cursor++) {
            if (*cursor < '0' || *cursor > '9' ||
                seed > (UINT32_MAX - (unsigned long)(*cursor - '0')) / 10) {
                fputs("HM_SEED must be random or a decimal uint32\n", stderr);
                return 2;
            }
            seed = seed * 10 + (unsigned long)(*cursor - '0');
        }
    }

    PyConfig config;
    PyConfig_InitPythonConfig(&config);
    config.use_hash_seed = fixed;
    config.hash_seed = seed;

    /* SetBytesArgv implicitly preinitializes using the original command-line
     * arguments, including -I and encoding-related switches. */
    PyStatus status = PyConfig_SetBytesArgv(&config, argc, argv);
    if (!PyStatus_Exception(status)) {
        status = Py_InitializeFromConfig(&config);
    }
    PyConfig_Clear(&config);
    if (PyStatus_IsExit(status)) {
        return status.exitcode;
    }
    if (PyStatus_Exception(status)) {
        Py_ExitStatusException(status);
    }
    return Py_RunMain();
}
#endif
