using System.Globalization;
using System.Numerics;

namespace Serk.Platform.Numerics;

/// <summary>
/// The only text forms the kernel accepts. No exponents, no leading '+', no leading zeros, no whitespace,
/// no thousands separators, no locale, bounded length:
/// <code>
/// integer  = "0" | ["-"] nonzero digit*
/// decimal  = ["-"] ("0" | nonzero digit*) "." digit+
/// rational = integer "/" nonzero digit*          (any reduced or unreduced fraction; result is reduced)
/// </code>
/// "-0" and "-0.0" are refused (one spelling per value). Anything else throws <see cref="FormatException"/>.
/// </summary>
public static class StrictGrammar
{
    public const int MaxLength = 400;

    public static Rational ParseRational(string? text)
    {
        if (text is null) throw new FormatException("no number");
        if (text.Length == 0 || text.Length > MaxLength) throw new FormatException($"a number is 1 to {MaxLength} characters");
        int slash = text.IndexOf('/');
        if (slash >= 0)
        {
            var n = ParseInteger(text[..slash]);
            var dText = text[(slash + 1)..];
            if (dText.Length == 0 || dText[0] == '0' || dText[0] == '-') throw new FormatException($"\"{text}\": the denominator is a positive integer without leading zeros");
            return Rational.Create(n, ParseInteger(dText));
        }
        return ParseDecimal(text);
    }

    /// <summary>A plain decimal (no fraction bar).</summary>
    public static Rational ParseDecimal(string text)
    {
        if (text.Length == 0 || text.Length > MaxLength) throw new FormatException($"a number is 1 to {MaxLength} characters");
        int dot = text.IndexOf('.');
        if (dot < 0) return Rational.FromInteger(ParseInteger(text));
        var intPart = text[..dot];
        var frac = text[(dot + 1)..];
        if (frac.Length == 0 || !AllDigits(frac)) throw new FormatException($"\"{text}\" is not a plain decimal");
        bool neg = intPart.StartsWith('-');
        var magnitude = ParseInteger(neg ? intPart[1..] : intPart, allowSign: false);
        var units = magnitude * BigInteger.Pow(10, frac.Length) + BigInteger.Parse(frac, NumberStyles.None, CultureInfo.InvariantCulture);
        if (neg && units.IsZero) throw new FormatException($"\"{text}\": write zero without a sign");
        return Rational.Create(neg ? -units : units, BigInteger.Pow(10, frac.Length));
    }

    public static BigInteger ParseInteger(string text, bool allowSign = true)
    {
        var digits = allowSign && text.StartsWith('-') ? text[1..] : text;
        if (digits.Length == 0 || !AllDigits(digits)) throw new FormatException($"\"{text}\" is not an integer");
        if (digits.Length > 1 && digits[0] == '0') throw new FormatException($"\"{text}\" has a leading zero");
        if (digits == "0" && digits.Length != text.Length) throw new FormatException($"\"{text}\": write zero without a sign");
        var v = BigInteger.Parse(digits, NumberStyles.None, CultureInfo.InvariantCulture);
        return digits.Length == text.Length ? v : -v;
    }

    private static bool AllDigits(string s)
    {
        foreach (var c in s) if (c is < '0' or > '9') return false;
        return true;
    }
}
