using System.Text;

namespace Serk.Platform.Numerics;

/// <summary>
/// A physical dimension as exponents of the seven SI base quantities: length L, mass M, time T, electric
/// current I, temperature Θ, amount N and luminous intensity J. Multiplication adds exponents, division
/// subtracts them; overflow of an exponent is an error, never a wrap-around.
/// </summary>
public readonly record struct Dimension(sbyte L, sbyte M, sbyte T, sbyte I, sbyte Theta, sbyte N, sbyte J)
{
    public static readonly Dimension None;
    public static readonly Dimension Length = new(1, 0, 0, 0, 0, 0, 0);
    public static readonly Dimension Mass = new(0, 1, 0, 0, 0, 0, 0);
    public static readonly Dimension Time = new(0, 0, 1, 0, 0, 0, 0);
    public static readonly Dimension Area = new(2, 0, 0, 0, 0, 0, 0);
    public static readonly Dimension Volume = new(3, 0, 0, 0, 0, 0, 0);
    public static readonly Dimension Force = new(1, 1, -2, 0, 0, 0, 0);
    public static readonly Dimension Pressure = new(-1, 1, -2, 0, 0, 0, 0);
    public static readonly Dimension Density = new(-3, 1, 0, 0, 0, 0, 0);

    public bool IsDimensionless => this == None;

    public static Dimension operator *(Dimension a, Dimension b) => new(Add(a.L, b.L), Add(a.M, b.M), Add(a.T, b.T), Add(a.I, b.I), Add(a.Theta, b.Theta), Add(a.N, b.N), Add(a.J, b.J));
    public static Dimension operator /(Dimension a, Dimension b) => new(Add(a.L, (sbyte)-b.L), Add(a.M, (sbyte)-b.M), Add(a.T, (sbyte)-b.T), Add(a.I, (sbyte)-b.I), Add(a.Theta, (sbyte)-b.Theta), Add(a.N, (sbyte)-b.N), Add(a.J, (sbyte)-b.J));

    private static sbyte Add(sbyte a, sbyte b) => checked((sbyte)(a + b));

    /// <summary>Canonical text, e.g. "L2", "L1.M1.T-2", "1" for dimensionless.</summary>
    public string ToCanonicalString()
    {
        var sb = new StringBuilder();
        void Part(string s, sbyte e) { if (e != 0) { if (sb.Length > 0) sb.Append('.'); sb.Append(s).Append(e); } }
        Part("L", L); Part("M", M); Part("T", T); Part("I", I); Part("Θ", Theta); Part("N", N); Part("J", J);
        return sb.Length == 0 ? "1" : sb.ToString();
    }

    public override string ToString() => ToCanonicalString();
}

/// <summary>A unit: a symbol, its dimension and its exact factor to the coherent SI unit.</summary>
public sealed record Unit(string Symbol, Dimension Dimension, Rational ToSi)
{
    public static readonly Unit One = new("1", Dimension.None, Rational.One);
    public static readonly Unit Metre = new("m", Dimension.Length, 1);
    public static readonly Unit Millimetre = new("mm", Dimension.Length, Rational.Create(1, 1000));
    public static readonly Unit Centimetre = new("cm", Dimension.Length, Rational.Create(1, 100));
    public static readonly Unit Kilometre = new("km", Dimension.Length, 1000);
    public static readonly Unit SquareMetre = new("m2", Dimension.Area, 1);
    public static readonly Unit CubicMetre = new("m3", Dimension.Volume, 1);
    public static readonly Unit Litre = new("L", Dimension.Volume, Rational.Create(1, 1000));
    public static readonly Unit Kilogram = new("kg", Dimension.Mass, 1);
    public static readonly Unit Tonne = new("t", Dimension.Mass, 1000);
    public static readonly Unit Second = new("s", Dimension.Time, 1);
    public static readonly Unit Hour = new("h", Dimension.Time, 3600);
    public static readonly Unit Day = new("d", Dimension.Time, 86400);
    public static readonly Unit Newton = new("N", Dimension.Force, 1);
    public static readonly Unit Kilonewton = new("kN", Dimension.Force, 1000);
    public static readonly Unit Megapascal = new("MPa", Dimension.Pressure, 1_000_000);
    public static readonly Unit KilogramPerCubicMetre = new("kg/m3", Dimension.Density, 1);

    private static readonly Dictionary<string, Unit> BySymbol = new[]
    {
        One, Metre, Millimetre, Centimetre, Kilometre, SquareMetre, CubicMetre, Litre, Kilogram, Tonne, Second, Hour, Day,
        Newton, Kilonewton, Megapascal, KilogramPerCubicMetre,
    }.ToDictionary(u => u.Symbol, StringComparer.Ordinal);

    /// <summary>A unit by its exact symbol; unknown symbols are refused (no guessing, no case folding).</summary>
    public static Unit FromSymbol(string symbol) => BySymbol.TryGetValue(symbol, out var u) ? u : throw new FormatException($"unknown unit \"{symbol}\"");
}
