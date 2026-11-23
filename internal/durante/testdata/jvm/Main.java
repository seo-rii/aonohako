import java.io.*;
public class Main { public static void main(String[] args) throws Exception { int n = Integer.parseInt(new BufferedReader(new InputStreamReader(System.in)).readLine().trim()); int x = 1; for (int i = 0; i < n; i++) x = x * 1664525 + 1013904223; System.out.println(Integer.toUnsignedString(x)); } }
