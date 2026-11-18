namespace Serk.Platform.Numerics;

/// <summary>An ISO 4217 currency and its minor-unit exponent. The registry mirrors pkg/money/currency.go.</summary>
public sealed record Currency(string Code, int Numeric, int MinorUnits, string Name)
{
    private static readonly Dictionary<string, Currency> Registry = new Currency[]
    {
        new("ETB", 230, 2, "Ethiopian Birr"), new("KES", 404, 2, "Kenyan Shilling"), new("UGX", 800, 0, "Ugandan Shilling"),
        new("TZS", 834, 2, "Tanzanian Shilling"), new("RWF", 646, 0, "Rwandan Franc"), new("NGN", 566, 2, "Nigerian Naira"),
        new("GHS", 936, 2, "Ghanaian Cedi"), new("ZAR", 710, 2, "South African Rand"), new("EGP", 818, 2, "Egyptian Pound"),
        new("MAD", 504, 2, "Moroccan Dirham"), new("XOF", 952, 0, "CFA Franc BCEAO"), new("XAF", 950, 0, "CFA Franc BEAC"),
        new("DJF", 262, 0, "Djiboutian Franc"), new("SDG", 938, 2, "Sudanese Pound"), new("SSP", 728, 2, "South Sudanese Pound"),
        new("SOS", 706, 2, "Somali Shilling"), new("ERN", 232, 2, "Eritrean Nakfa"), new("MZN", 943, 2, "Mozambican Metical"),
        new("ZMW", 967, 2, "Zambian Kwacha"), new("BWP", 72, 2, "Pula"), new("NAD", 516, 2, "Namibia Dollar"),
        new("AOA", 973, 2, "Kwanza"), new("CDF", 976, 2, "Congolese Franc"), new("BIF", 108, 0, "Burundi Franc"),
        new("MWK", 454, 2, "Malawi Kwacha"), new("MUR", 480, 2, "Mauritius Rupee"), new("TND", 788, 3, "Tunisian Dinar"),
        new("DZD", 12, 2, "Algerian Dinar"), new("LYD", 434, 3, "Libyan Dinar"), new("USD", 840, 2, "US Dollar"),
        new("EUR", 978, 2, "Euro"), new("GBP", 826, 2, "Pound Sterling"), new("CNY", 156, 2, "Yuan Renminbi"),
        new("AED", 784, 2, "UAE Dirham"), new("SAR", 682, 2, "Saudi Riyal"), new("INR", 356, 2, "Indian Rupee"),
        new("JPY", 392, 0, "Yen"), new("TRY", 949, 2, "Turkish Lira"), new("KWD", 414, 3, "Kuwaiti Dinar"),
        new("BHD", 48, 3, "Bahraini Dinar"), new("CHF", 756, 2, "Swiss Franc"),
    }.ToDictionary(c => c.Code, StringComparer.Ordinal);

    /// <summary>Case-insensitive lookup, as pkg/money.Lookup.</summary>
    public static Currency Of(string code) =>
        Registry.TryGetValue(code.ToUpperInvariant(), out var c) ? c : throw new UnknownCurrencyException(code);

    public static IReadOnlyCollection<Currency> All => Registry.Values;
}

public sealed class UnknownCurrencyException(string code) : Exception($"unknown currency \"{code}\"");
