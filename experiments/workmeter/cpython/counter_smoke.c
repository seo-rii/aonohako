#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
__attribute__((const,noinline)) static uint64_t pure_work(uint64_t n){
 uint64_t sum=0;
 for(uint64_t i=0;i<n;i++)sum+=((i*UINT64_C(0x9e3779b97f4a7c15))^(i>>7))%UINT64_C(1000000007);
 return sum;
}
int main(int argc,char **argv){
 if(argc!=2&&argc!=3)return 2;
 const uint64_t n=strtoull(argv[1],0,10);
 if(argc==3){if(strcmp(argv[2],"pure"))return 3;printf("%llu\n",(unsigned long long)pure_work(n));return 0;}
 volatile uint64_t checksum=0;
 for(uint64_t i=0;i<n;i++)checksum+=i*3+1;
 printf("%llu\n",(unsigned long long)checksum);
 return 0;
}
