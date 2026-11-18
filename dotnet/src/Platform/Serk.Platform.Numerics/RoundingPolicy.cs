namespace Serk.Platform.Numerics;

/// <summary>
/// A named, versioned rounding rule: the mode and the number of decimal places of the one rounding step a
/// calculation may take. A policy never changes once published; a different rule is a new version, and every
/// recorded calculation names the policy (id@version) it used.
/// </summary>
public sealed record RoundingPolicy(string Id, int Version, RoundingMode Mode, int Scale)
{
    public string Key => $"{Id}@{Version}";

    public Rational Apply(Rational value) => value.Round(Scale, Mode);

    /// <summary>Money to the currency's minor unit, ties to even (the Go kernel's default HALF_EVEN).</summary>
    public static RoundingPolicy MinorUnitHalfEven(int scale) => new("minor-unit-half-even", 1, RoundingMode.HalfEven, scale);
}
