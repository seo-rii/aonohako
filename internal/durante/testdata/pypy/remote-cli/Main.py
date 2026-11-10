import sys
x=0
for i in range(250000): x=(x*1664525+i+1013904223)&0xffffffff
print(x)
