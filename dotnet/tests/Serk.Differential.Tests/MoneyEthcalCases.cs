using System.Globalization;
using System.Numerics;
using System.Text;

namespace Serk.Differential.Tests;

/// <summary>
/// The money and Ethiopian-calendar case set: hand-picked edges plus seeded random cases. The seed is fixed,
/// so the set (and the Go recording) is the same on every machine.
/// </summary>
public static class MoneyEthcalCases
{
    public const int Seed = 20261118;
    private static readonly string[] Policies = ["HALF_EVEN", "HALF_UP", "DOWN", "UP"];
    private static readonly string[] Currencies = ["ETB", "USD", "JPY", "KWD", "TND", "UGX"];

    public static IReadOnlyList<Case> Build()
    {
        var rnd = new Random(Seed);
        var cases = new List<Case>();
        int n = 0;
        void Add(string op, Dictionary<string, object> args) => cases.Add(new Case($"{op}#{++n}", op, args));

        // Strict amounts: edges, then random spellings (valid and not).
        foreach (var (a, c) in new[] { ("0", "ETB"), ("0.00", "ETB"), ("-0.00", "ETB"), ("-0", "ETB"), ("007.50", "ETB"), ("1.234", "ETB"), ("1.5", "JPY"),
                     ("1.234", "KWD"), ("1.2345", "KWD"), ("123456789012345678", "ETB"), ("1234567890123456789", "ETB"), ("1e3", "ETB"), (".5", "ETB"),
                     ("5.", "ETB"), ("+5", "ETB"), (" 5", "ETB"), ("5", "etb"), ("5", "XXX"), ("5", "ETBB"), ("-12.30", "USD"), ("1.000001", "KWD") })
            Add("money.parse_strict", new() { ["amount"] = a, ["currency"] = c });
        for (int i = 0; i < 600; i++)
            Add("money.parse_strict", new() { ["amount"] = RandomSpelling(rnd), ["currency"] = Pick(rnd, Currencies) });

        // Rounding: ties are generated on purpose (…5 exactly at the next place).
        foreach (var p in Policies.Concat(["", "BANKERS"]))
            foreach (var a in new[] { "2.5", "-2.5", "3.5", "0.005", "-0.005", "0.015", "1.0049999999999999999999", "-1.0050000000000000000001" })
                Add("money.round", new() { ["amount"] = a, ["places"] = a.Contains("0.0") || a.StartsWith("1.0") || a.StartsWith("-1.0") ? 2 : 0, ["policy"] = p });
        for (int i = 0; i < 1500; i++)
        {
            int places = rnd.Next(0, 5);
            var a = rnd.Next(4) == 0 ? Tie(rnd, places) : PlainDecimal(rnd, rnd.Next(1, 21), rnd.Next(0, 13));
            Add("money.round", new() { ["amount"] = a, ["places"] = places, ["policy"] = Pick(rnd, Policies) });
        }

        // Multiplication and division (division also by values that expose double rounding).
        Add("money.div", new() { ["amount"] = "0.01", ["currency"] = "ETB", ["factor"] = "2.0000000000000000001", ["policy"] = "HALF_UP" });
        Add("money.div", new() { ["amount"] = "1.00", ["currency"] = "ETB", ["factor"] = "0", ["policy"] = "HALF_EVEN" });
        Add("money.mul", new() { ["amount"] = "1.00", ["currency"] = "ETB", ["factor"] = "1.5", ["policy"] = "NEAREST" });
        for (int i = 0; i < 1200; i++)
        {
            var cur = Pick(rnd, Currencies);
            var amount = Amount(rnd, cur);
            Add(i % 2 == 0 ? "money.mul" : "money.div", new()
            {
                ["amount"] = amount, ["currency"] = cur, ["policy"] = Pick(rnd, Policies),
                ["factor"] = rnd.Next(5) == 0 ? NearHalfDivisor(rnd) : PlainDecimal(rnd, rnd.Next(1, 7), rnd.Next(0, 9), nonZero: true),
            });
        }
        // Division where the 16-place intermediate of pkg/money lands on a tie.
        for (int i = 0; i < 200; i++)
            Add("money.div", new() { ["amount"] = "0.01", ["currency"] = "ETB", ["factor"] = NearHalfDivisor(rnd), ["policy"] = Pick(rnd, Policies) });

        // Allocation, including amounts and ratios large enough to overflow 64-bit arithmetic.
        Add("money.allocate", new() { ["amount"] = "100.00", ["currency"] = "ETB", ["ratios"] = new long[] { 1, 1, 1 } });
        Add("money.allocate", new() { ["amount"] = "100.00", ["currency"] = "ETB", ["ratios"] = Array.Empty<long>() });
        Add("money.allocate", new() { ["amount"] = "100.00", ["currency"] = "ETB", ["ratios"] = new long[] { 0, 0 } });
        Add("money.allocate", new() { ["amount"] = "100.00", ["currency"] = "ETB", ["ratios"] = new long[] { 2, -1 } });
        Add("money.allocate", new() { ["amount"] = "999999999999999999.99", ["currency"] = "ETB", ["ratios"] = new long[] { 1, 1, 1 } });
        Add("money.allocate", new() { ["amount"] = "1000000000000.00", ["currency"] = "ETB", ["ratios"] = new long[] { 100000, 33333, 66667 } });
        // Realistic magnitudes (|minor units| × ratio < 2^63): no overflow in Go.
        for (int i = 0; i < 800; i++)
        {
            var cur = Pick(rnd, Currencies);
            var ratios = Enumerable.Range(0, rnd.Next(1, 7)).Select(_ => (long)rnd.Next(0, rnd.Next(2) == 0 ? 1000 : 1_000_000)).ToArray();
            if (ratios.Sum() == 0) ratios[0] = 1;
            Add("money.allocate", new() { ["amount"] = PlainDecimal(rnd, rnd.Next(1, 11), cur is "JPY" or "UGX" ? 0 : 2), ["currency"] = cur is "KWD" or "TND" ? "ETB" : cur, ["ratios"] = ratios });
        }
        // Deliberate overflow of pkg/money's int64 arithmetic (DIFF-002).
        for (int i = 0; i < 10; i++)
            Add("money.allocate", new() { ["amount"] = PlainDecimal(rnd, rnd.Next(12, 19), 2), ["currency"] = "ETB", ["ratios"] = new long[] { rnd.Next(1, 3_000_000), rnd.Next(1, 3_000_000), rnd.Next(1, 3_000_000) } });

        // Ethiopian calendar.
        for (int i = 0; i < 600; i++) Add("eth.from_jdn", new() { ["jdn"] = rnd.Next(1724221, 5_400_000) });
        foreach (var g in new[] { "1970-01-01", "2000-02-29", "2023-09-12", "2026-09-11", "2026-11-16", "1900-03-01", "9999-12-31", "1000-01-01" })
            Add("eth.from_gregorian", new() { ["text"] = g });
        for (int i = 0; i < 600; i++)
            Add("eth.from_gregorian", new() { ["text"] = new DateOnly(1000, 1, 1).AddDays(rnd.Next(0, 3_287_000)).ToString("yyyy-MM-dd", CultureInfo.InvariantCulture) });
        foreach (var (y, m, d) in new[] { (2015, 13, 6), (2016, 13, 6), (2016, 13, 5), (1, 1, 1), (9999, 13, 5), (9991, 1, 1), (9992, 1, 1), (0, 1, 1), (2016, 14, 1), (2016, 1, 31), (2016, 1, 0) })
            Add("eth.to_gregorian", new() { ["y"] = y, ["m"] = m, ["d"] = d });
        for (int i = 0; i < 600; i++)
            Add("eth.to_gregorian", new() { ["y"] = rnd.Next(1, 9993), ["m"] = rnd.Next(0, 15), ["d"] = rnd.Next(0, 32) });
        foreach (var t in new[] { "2016-01-01", "2015-13-06", "2016-13-06", "+016-01-01", "-016-01-01", " 016-01-01", "2016-1-001", "2016/01/01", "20160101  ", "2016-01-1x", "0000-01-01", "9999-13-05" })
            Add("eth.parse", new() { ["text"] = t });
        for (int i = 0; i < 300; i++)
            Add("eth.parse", new() { ["text"] = string.Create(CultureInfo.InvariantCulture, $"{rnd.Next(0, 10000):D4}-{rnd.Next(0, 15):D2}-{rnd.Next(0, 32):D2}") });
        return cases;
    }

    private static string Pick(Random r, string[] a) => a[r.Next(a.Length)];

    private static string Digits(Random r, int count, bool noLeadingZero)
    {
        var sb = new StringBuilder();
        for (int i = 0; i < count; i++) sb.Append((char)('0' + (i == 0 && noLeadingZero ? r.Next(1, 10) : r.Next(10))));
        return sb.ToString();
    }

    /// <summary>A canonical plain decimal (the kernel's strict grammar).</summary>
    public static string PlainDecimal(Random r, int intDigits, int decimals, bool nonZero = false)
    {
        string s;
        do
        {
            var ip = r.Next(3) == 0 ? "0" : Digits(r, intDigits, noLeadingZero: true);
            s = decimals > 0 ? ip + "." + Digits(r, decimals, false) : ip;
        } while ((nonZero || r.Next(2) == 0) && s.Trim('0', '.').Length == 0);
        return s.Trim('0', '.').Length > 0 && r.Next(3) == 0 ? "-" + s : s;
    }

    private static string Tie(Random r, int places)
    {
        var s = PlainDecimal(r, r.Next(1, 10), places);
        return (s.Contains('.') ? s : s + ".") + "5";
    }

    private static string Amount(Random r, string cur)
    {
        int minor = cur is "JPY" or "UGX" ? 0 : cur is "KWD" or "TND" ? 3 : 2;
        return PlainDecimal(r, r.Next(1, 19), r.Next(0, minor + 1));
    }

    /// <summary>A divisor within 10^-17 of 2 or 4 (x/d then sits within 10^-17 of a tie at the minor unit).</summary>
    private static string NearHalfDivisor(Random r)
    {
        var whole = r.Next(2) == 0 ? "2" : "4";
        var tail = new string('0', r.Next(16, 20)) + r.Next(1, 10).ToString(CultureInfo.InvariantCulture);
        return r.Next(2) == 0 ? $"{whole}.{tail}" : $"{(whole == "2" ? "1" : "3")}.{new string('9', tail.Length - 1)}{r.Next(1, 10)}";
    }

    /// <summary>Spellings for the strict parser: canonical, leading zeros, signs, exponents, blanks, too many digits.</summary>
    private static string RandomSpelling(Random r) => r.Next(10) switch
    {
        0 => "0" + PlainDecimal(r, r.Next(1, 4), r.Next(0, 3)).TrimStart('-'),
        1 => "-0" + (r.Next(2) == 0 ? "" : "." + new string('0', r.Next(1, 4))),
        2 => PlainDecimal(r, r.Next(1, 4), 0) + "e" + r.Next(0, 4),
        3 => " " + PlainDecimal(r, 2, 2),
        4 => PlainDecimal(r, r.Next(17, 21), r.Next(0, 3)),
        5 => PlainDecimal(r, r.Next(1, 4), r.Next(3, 8)),
        _ => PlainDecimal(r, r.Next(1, 19), r.Next(0, 4)),
    };
}
