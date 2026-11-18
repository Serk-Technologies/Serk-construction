using System.Globalization;
using System.Numerics;
using System.Text.Json;
using Serk.Platform.Numerics;
using Serk.Platform.Primitives;

namespace Serk.Differential.Tests;

/// <summary>Evaluates the same cases with the C# kernel, producing the oracle's canonical outcome shape.</summary>
public static class CSharpSide
{
    public static RoundingMode? Mode(string policy) => policy switch
    {
        "" or "HALF_EVEN" => RoundingMode.HalfEven, "HALF_UP" => RoundingMode.HalfUp, "DOWN" => RoundingMode.Down, "UP" => RoundingMode.Up, _ => null,
    };

    public static string S(Case c, string k) => c.Args[k] switch { string s => s, JsonElement e => e.GetString()!, var o => Convert.ToString(o, CultureInfo.InvariantCulture)! };
    public static int I(Case c, string k) => Convert.ToInt32(c.Args[k], CultureInfo.InvariantCulture);

    public static Outcome Evaluate(Case c)
    {
        try { return Run(c); }
        catch (FormatException) { return Outcome.Fail(c.Op.StartsWith("eth.", StringComparison.Ordinal) ? "invalid_date" : "invalid_amount"); }
        catch (UnknownCurrencyException) { return Outcome.Fail("unknown_currency"); }
        catch (DivideByZeroException) { return Outcome.Fail("division_by_zero"); }
        catch (ArgumentException e) when (e.Message.Contains("ratio", StringComparison.Ordinal)) { return Outcome.Fail("invalid_ratios"); }
        catch (ArgumentOutOfRangeException) { return Outcome.Fail("out_of_range"); }
    }

    private static Outcome Run(Case c)
    {
        switch (c.Op)
        {
            case "money.parse_strict":
            {
                var cur = Currency.Of(S(c, "currency"));
                var m = Money.Parse(S(c, "amount"), cur);
                return Outcome.Of($"{m.AmountText} {cur.Code}");
            }
            case "money.round":
            {
                var mode = Mode(S(c, "policy"));
                if (mode is null) return Outcome.Fail("unknown_rounding");
                int places = I(c, "places");
                return Outcome.Of(Rational.Parse(S(c, "amount")).Round(places, mode.Value).ToFixedString(places));
            }
            case "money.mul":
            case "money.div":
            {
                var cur = Currency.Of(S(c, "currency"));
                var m = Money.Parse(S(c, "amount"), cur);
                var f = Rational.Parse(S(c, "factor"));
                if (c.Op == "money.div" && f.IsZero) return Outcome.Fail("division_by_zero");
                var mode = Mode(S(c, "policy"));
                if (mode is null) return Outcome.Fail("unknown_rounding");
                return Outcome.Of((c.Op == "money.mul" ? m.Multiply(f, mode.Value) : m.Divide(f, mode.Value)).AmountText);
            }
            case "money.allocate":
            {
                var cur = Currency.Of(S(c, "currency"));
                var m = Money.Parse(S(c, "amount"), cur);
                var parts = m.Allocate(Ratios(c).Select(x => new BigInteger(x)).ToArray());
                return Outcome.Of(string.Join("|", parts.Select(p => p.AmountText)));
            }
            case "eth.from_jdn":
                return Outcome.Of(EthiopianDate.FromJdn(I(c, "jdn")).Iso);
            case "eth.from_gregorian":
                return Outcome.Of(EthiopianDate.FromGregorian(DateOnly.ParseExact(S(c, "text"), "yyyy-MM-dd", CultureInfo.InvariantCulture)).Iso);
            case "eth.to_gregorian":
            {
                var e = new EthiopianDate(I(c, "y"), I(c, "m"), I(c, "d"));
                if (!e.IsValid) return Outcome.Fail("invalid_date");
                return Outcome.Of(e.ToGregorian().ToString("yyyy-MM-dd", CultureInfo.InvariantCulture));
            }
            case "eth.parse":
            {
                var e = EthiopianDate.Parse(S(c, "text"));
                return Outcome.Of($"{e.Iso} {e}");
            }
        }
        throw new InvalidOperationException($"unknown op {c.Op}");
    }

    public static long[] Ratios(Case c) => c.Args["ratios"] switch
    {
        long[] a => a,
        JsonElement e => e.EnumerateArray().Select(x => x.GetInt64()).ToArray(),
        var o => throw new InvalidOperationException($"ratios as {o.GetType()}"),
    };
}
