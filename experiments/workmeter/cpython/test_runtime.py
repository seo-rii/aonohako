"""Small native counter/protocol gates; no CPython rebuild or cloud access."""
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
PROGRAM = r'''
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <errno.h>
#include <sys/wait.h>
#include <unistd.h>
extern void __wm_register(const char *,uint64_t *,const uint64_t *,uint64_t);
extern void __wm_overflow(void);
static uint64_t first[]={2,0,5}, second[]={1,2,0};
static const uint64_t weights[]={3,7,11};
static void *worker(void *unused) {(void)unused;return NULL;}
int main(int argc,char **argv) {
    if(argc!=2)return 2;
    if(strcmp(argv[1],"missing")==0)return 0;
    __wm_register("same-metadata",first,weights,3);
    if(strcmp(argv[1],"two-modules")==0)
        __wm_register("same-metadata",second,weights,3);
    if(strcmp(argv[1],"duplicate")==0)
        __wm_register("same-metadata",first,weights,3);
    if(strcmp(argv[1],"invalid")==0)
        __wm_register(NULL,first,weights,3);
    if(strcmp(argv[1],"overflow")==0)__wm_overflow();
    if(strcmp(argv[1],"total-overflow")==0)first[0]=UINT64_MAX;
    if(strcmp(argv[1],"thread")==0) {
        pthread_t thread;
        if(pthread_create(&thread,NULL,worker,NULL)!=ENOTSUP)return 3;
    }
    if(strcmp(argv[1],"fork")==0) {
        pid_t child=fork();
        if(child<0)return 4;
        if(child==0)exit(0);
        int status;
        if(waitpid(child,&status,0)<0 || !WIFEXITED(status))return 5;
    }
    if(strcmp(argv[1],"early-exit")==0)_exit(0);
    if(strcmp(argv[1],"wait")==0) {
        puts("ready");fflush(stdout);
        for(;;)pause();
    }
    return 0;
}
'''


class RuntimeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='workmeter-native-')
        cls.root = Path(cls.directory.name)
        cls.executable = cls.root/'test-runtime'
        (cls.root/'probe.c').write_text(PROGRAM)
        cc=shlex.split(os.environ.get('CC','cc'))
        subprocess.run([*cc,'-O2','-Wall','-Wextra','-Werror','-shared','-fPIC',
            str(ROOT/'runtime.c'),'-o',str(cls.root/'libworkmeter.so'),'-ldl','-pthread'],check=True)
        subprocess.run([*cc,'-O2','-Wall','-Wextra','-Werror',str(cls.root/'probe.c'),
            '-o',str(cls.executable),'-L'+str(cls.root),'-Wl,-rpath,'+str(cls.root),
            '-lworkmeter','-pthread'],check=True)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def invoke(self, mode, expected=True):
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            report=Path(directory)/'report.json'
            env=dict(os.environ,WM_REPORT_PATH=str(report),WM_EXPECT_METER=str(int(expected)),
                     WM_RUN_ID='quoted"\nidentifier')
            child=subprocess.Popen([str(self.executable),mode],env=env)
            self.assertEqual(child.wait(timeout=10),0)
            data=json.loads(report.read_text()) if report.exists() else None
            if data is not None:
                self.assertEqual(data['pid'],child.pid)
                self.assertEqual(data['run_id'],env['WM_RUN_ID'])
                self.assertEqual(report.stat().st_mode & 0o777,0o600)
                self.assertEqual(data['authority'],'diagnostic_only')
            return data

    def test_weighted_count_and_distinct_arrays_with_same_metadata(self):
        one=self.invoke('normal');two=self.invoke('two-modules')
        self.assertTrue(one['valid']);self.assertEqual(one['work_units'],61)
        self.assertTrue(two['valid']);self.assertEqual(two['work_units'],78)
        self.assertEqual(two['modules_seen'],2)

    def test_integrity_and_unsupported_process_models_are_not_zero(self):
        for mode,flag in (('duplicate',4),('invalid',2),('overflow',1),
                          ('total-overflow',16),('thread',8),('fork',64),('missing',32)):
            with self.subTest(mode=mode):
                data=self.invoke(mode)
                self.assertFalse(data['valid'])
                self.assertTrue(data['flags'] & flag)
                self.assertIsNone(data['work_units'])
                self.assertTrue(all(not m['counts'] for m in data['modules']))

    def test_unmetered_control_can_report_zero(self):
        data=self.invoke('missing',expected=False)
        self.assertTrue(data['valid']);self.assertEqual(data['work_units'],0)

    def test_early_exit_is_missing_not_valid_zero(self):
        self.assertIsNone(self.invoke('early-exit'))

    def test_sigkill_has_no_completed_report(self):
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            report=Path(directory)/'report.json'
            child=subprocess.Popen([str(self.executable),'wait'],stdout=subprocess.PIPE,
                env=dict(os.environ,WM_REPORT_PATH=str(report),WM_EXPECT_METER='1'))
            try:
                self.assertEqual(child.stdout.readline(),b'ready\n')
                child.kill();self.assertEqual(child.wait(timeout=10),-signal.SIGKILL)
                self.assertFalse(report.exists())
            finally:
                if child.poll() is None:child.kill();child.wait(timeout=10)
                child.stdout.close()

    def test_existing_report_is_never_overwritten(self):
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            report=Path(directory)/'report.json';report.write_text('existing')
            subprocess.run([str(self.executable),'normal'],check=True,timeout=10,
                env=dict(os.environ,WM_REPORT_PATH=str(report),WM_EXPECT_METER='1'))
            self.assertEqual(report.read_text(),'existing')


if __name__=='__main__':unittest.main()
