using System.Globalization;

namespace Serk.Platform.Primitives;

/// <summary>
/// A date in the Ethiopian calendar: 12 months of 30 days and Pagume of 5 days (6 in a leap year, every year
/// with year mod 4 == 3). Converted through the Julian Day Number, as pkg/ethcal does; the differential
/// harness checks the two against each other.
/// </summary>
public readonly record struct EthiopianDate(int Year, int Month, int Day)
{
    private const int Epoch = 1723856;   // JDN of 1 Meskerem 1 (Amete Mihret), minus one day of offset handling below
    private const int UnixJdn = 2440588; // JDN of 1970-01-01

    public static readonly IReadOnlyList<string> MonthNames =
        ["Meskerem", "Tikimt", "Hidar", "Tahsas", "Tir", "Yekatit", "Megabit", "Miyazya", "Ginbot", "Sene", "Hamle", "Nehase", "Pagume"];

    public static bool IsLeap(int year) => Mod(year, 4) == 3;

    public static int DaysIn(int year, int month) => month switch
    {
        >= 1 and <= 12 => 30,
        13 => IsLeap(year) ? 6 : 5,
        _ => 0,
    };

    public bool IsValid => Year is >= 1 and <= 9999 && Month is >= 1 and <= 13 && Day >= 1 && Day <= DaysIn(Year, Month);

    public int Jdn => Epoch + 365 + 365 * (Year - 1) + Year / 4 + 30 * Month + Day - 31;

    public static EthiopianDate FromJdn(int jdn)
    {
        int r = Mod(jdn - Epoch, 1461);
        int n = Mod(r, 365) + 365 * (r / 1460);
        int y = 4 * FloorDiv(jdn - Epoch, 1461) + r / 365 - r / 1460;
        return new EthiopianDate(y, n / 30 + 1, Mod(n, 30) + 1);
    }

    public static EthiopianDate FromGregorian(DateOnly date) => FromJdn(date.DayNumber - new DateOnly(1970, 1, 1).DayNumber + UnixJdn);

    public DateOnly ToGregorian()
    {
        if (!IsValid) throw new FormatException($"no such Ethiopian date: {Iso}");
        return new DateOnly(1970, 1, 1).AddDays(Jdn - UnixJdn);
    }

    public string Iso => string.Create(CultureInfo.InvariantCulture, $"{Year:D4}-{Month:D2}-{Day:D2}");

    public override string ToString() => Month is >= 1 and <= 13
        ? string.Create(CultureInfo.InvariantCulture, $"{Day} {MonthNames[Month - 1]} {Year}")
        : string.Create(CultureInfo.InvariantCulture, $"{Year}-{Month:D2}-{Day:D2}");

    /// <summary>Exactly "YYYY-MM-DD" in the Ethiopian calendar, digits only.</summary>
    public static EthiopianDate Parse(string text)
    {
        if (text is not { Length: 10 } || text[4] != '-' || text[7] != '-'
            || !int.TryParse(text.AsSpan(0, 4), NumberStyles.None, CultureInfo.InvariantCulture, out var y)
            || !int.TryParse(text.AsSpan(5, 2), NumberStyles.None, CultureInfo.InvariantCulture, out var m)
            || !int.TryParse(text.AsSpan(8, 2), NumberStyles.None, CultureInfo.InvariantCulture, out var d))
            throw new FormatException($"\"{text}\": write YYYY-MM-DD in the Ethiopian calendar");
        var e = new EthiopianDate(y, m, d);
        return e.IsValid ? e : throw new FormatException($"no such Ethiopian date: \"{text}\"");
    }

    private static int Mod(int a, int b) => ((a % b) + b) % b;
    private static int FloorDiv(int a, int b) => (a - Mod(a, b)) / b;
}
