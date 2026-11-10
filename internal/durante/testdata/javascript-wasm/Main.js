const n = Number(require("fs").readFileSync(0, "utf8").trim());
let x = 1;
for (let i = 0; i < n; ++i) x = (Math.imul(x, 1664525) + 1013904223) >>> 0;
console.log(x);
