using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace Serk.Platform.Numerics;

/// <summary>
/// Canonical serialization: one byte sequence per value, so equal values hash equally across processes,
/// machines and versions. A canonical value is null, a boolean, a string, an integer, a
/// <see cref="Rational"/> (written as its canonical string), a <see cref="Quantity"/>, a <see cref="Money"/>,
/// a list or a string-keyed map. Maps are written with keys in ordinal order; there is no whitespace, no
/// floating point and no culture.
/// </summary>
public static class Canonical
{
    public static string Serialize(object? value)
    {
        var sb = new StringBuilder();
        Write(sb, value);
        return sb.ToString();
    }

    public static string Sha256Hex(string canonical) => Convert.ToHexStringLower(SHA256.HashData(Encoding.UTF8.GetBytes(canonical)));

    private static void Write(StringBuilder sb, object? v)
    {
        switch (v)
        {
            case null: sb.Append("null"); break;
            case bool b: sb.Append(b ? "true" : "false"); break;
            case string s: WriteString(sb, s); break;
            case int or long or System.Numerics.BigInteger: sb.Append(Convert.ToString(v, CultureInfo.InvariantCulture)); break;
            case Rational r: WriteString(sb, r.ToCanonicalString()); break;
            case Quantity q:
                Write(sb, new SortedDictionary<string, object?>(StringComparer.Ordinal)
                {
                    ["dim"] = q.Dimension.ToCanonicalString(), ["si"] = q.SiValue, ["err"] = q.ErrorBound, ["exact"] = q.IsExact,
                });
                break;
            case Money m:
                Write(sb, new SortedDictionary<string, object?>(StringComparer.Ordinal) { ["amount"] = m.AmountText, ["currency"] = m.Currency.Code });
                break;
            case System.Collections.IDictionary map:
                var keys = new List<string>();
                foreach (var k in map.Keys) keys.Add(k as string ?? throw new ArgumentException("canonical map keys are strings"));
                keys.Sort(StringComparer.Ordinal);
                sb.Append('{');
                for (int i = 0; i < keys.Count; i++)
                {
                    if (i > 0) sb.Append(',');
                    WriteString(sb, keys[i]);
                    sb.Append(':');
                    Write(sb, map[keys[i]]);
                }
                sb.Append('}');
                break;
            case System.Collections.IEnumerable list:
                sb.Append('[');
                bool first = true;
                foreach (var item in list) { if (!first) sb.Append(','); first = false; Write(sb, item); }
                sb.Append(']');
                break;
            default: // includes binary and decimal floating point: use Rational
                throw new ArgumentException($"{v.GetType().Name} has no canonical form; exact values are Rational");
        }
    }

    private static void WriteString(StringBuilder sb, string s)
    {
        sb.Append('"');
        foreach (var c in s)
        {
            switch (c)
            {
                case '"': sb.Append("\\\""); break;
                case '\\': sb.Append("\\\\"); break;
                default:
                    if (c < 0x20) sb.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                    else sb.Append(c);
                    break;
            }
        }
        sb.Append('"');
    }
}
