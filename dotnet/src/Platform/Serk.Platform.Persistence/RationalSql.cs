using Npgsql;
using Serk.Platform.Numerics;

namespace Serk.Platform.Persistence;

/// <summary>
/// Reading and writing <see cref="Rational"/> values in <c>serk_rational</c> columns (dotnet/db/0001_serk_rational.sql):
/// always the canonical text, parsed back with Rational.ParseCanonical, so a value read is the value
/// written, bit for bit.
/// </summary>
public static class RationalSql
{
    public static NpgsqlParameter Parameter(string name, Rational value) => new(name, value.ToCanonicalString());

    public static Rational Read(NpgsqlDataReader reader, int ordinal)
    {
        ArgumentNullException.ThrowIfNull(reader);
        return Rational.ParseCanonical(reader.GetString(ordinal));
    }
}
