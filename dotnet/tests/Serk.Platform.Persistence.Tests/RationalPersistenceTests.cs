using System.Numerics;
using Npgsql;
using Serk.Platform.Numerics;
using Serk.Platform.Persistence;

namespace Serk.Platform.Persistence.Tests;

/// <summary>
/// Rational persistence on a real PostgreSQL server, in a fresh scratch database per run: values round-trip
/// exactly; the database refuses non-canonical spellings on its own; comparison is exact. Without
/// SERK_DOTNET_TEST_ADMIN_DSN the test fails (it is never skipped).
/// </summary>
public sealed class RationalPersistenceTests : IAsyncLifetime
{
    private string _db = "";
    private string _dsn = "";

    public async Task InitializeAsync()
    {
        var admin = Environment.GetEnvironmentVariable("SERK_DOTNET_TEST_ADMIN_DSN");
        Assert.False(string.IsNullOrEmpty(admin), "set SERK_DOTNET_TEST_ADMIN_DSN to a disposable PostgreSQL server (see dotnet/README.md)");
        _db = "serk_dotnet_" + Guid.NewGuid().ToString("N")[..12];
        await using (var c = new NpgsqlConnection(admin))
        {
            await c.OpenAsync();
            await new NpgsqlCommand($"CREATE DATABASE {_db}", c).ExecuteNonQueryAsync();
        }
        _dsn = new NpgsqlConnectionStringBuilder(admin) { Database = _db }.ConnectionString;
        await using var conn = new NpgsqlConnection(_dsn);
        await conn.OpenAsync();
        var sql = await File.ReadAllTextAsync(Path.Combine(Root(), "db", "0001_serk_rational.sql"));
        await new NpgsqlCommand(sql + "; CREATE TABLE q (id int PRIMARY KEY, v serk_rational NOT NULL);", conn).ExecuteNonQueryAsync();
    }

    public async Task DisposeAsync()
    {
        NpgsqlConnection.ClearAllPools();
        await using var c = new NpgsqlConnection(Environment.GetEnvironmentVariable("SERK_DOTNET_TEST_ADMIN_DSN"));
        await c.OpenAsync();
        await new NpgsqlCommand($"DROP DATABASE IF EXISTS {_db} WITH (FORCE)", c).ExecuteNonQueryAsync();
    }

    private static string Root()
    {
        for (var d = new DirectoryInfo(AppContext.BaseDirectory); d is not null; d = d.Parent)
            if (File.Exists(Path.Combine(d.FullName, "SerkOS.slnx"))) return d.FullName;
        throw new InvalidOperationException("SerkOS.slnx not found");
    }

    [Fact]
    public async Task Values_round_trip_exactly_including_huge_ones()
    {
        var rnd = new Random(7);
        var values = new List<Rational> { Rational.Zero, Rational.One, Rational.Create(-1, 3), Rational.Parse("9576.47"),
            Rational.Create(BigInteger.Pow(10, 400) + 1, BigInteger.Pow(3, 500)) };
        for (int i = 0; i < 200; i++) values.Add(Rational.Create(rnd.NextInt64(long.MinValue, long.MaxValue), rnd.NextInt64(1, long.MaxValue)));
        await using var conn = new NpgsqlConnection(_dsn);
        await conn.OpenAsync();
        for (int i = 0; i < values.Count; i++)
        {
            var ins = new NpgsqlCommand("INSERT INTO q VALUES (@id, @v)", conn);
            ins.Parameters.AddWithValue("id", i);
            ins.Parameters.Add(RationalSql.Parameter("v", values[i]));
            await ins.ExecuteNonQueryAsync();
        }
        await using var r = await new NpgsqlCommand("SELECT id, v FROM q ORDER BY id", conn).ExecuteReaderAsync();
        while (await r.ReadAsync()) Assert.Equal(values[r.GetInt32(0)], RationalSql.Read(r, 1));
    }

    [Theory]
    [InlineData("2/4")]
    [InlineData("1/1")]
    [InlineData("3/-4")]
    [InlineData("0/5")]
    [InlineData("-0")]
    [InlineData("007")]
    [InlineData("1.5")]
    [InlineData("1e3")]
    [InlineData(" 1")]
    [InlineData("1/0")]
    public async Task The_database_refuses_non_canonical_spellings(string text)
    {
        await using var conn = new NpgsqlConnection(_dsn);
        await conn.OpenAsync();
        var ins = new NpgsqlCommand("INSERT INTO q VALUES (1, @v)", conn);
        ins.Parameters.AddWithValue("v", text);
        var e = await Assert.ThrowsAsync<PostgresException>(() => ins.ExecuteNonQueryAsync());
        Assert.Equal("23514", e.SqlState); // check_violation
    }

    [Fact]
    public async Task Comparison_in_the_database_is_exact()
    {
        await using var conn = new NpgsqlConnection(_dsn);
        await conn.OpenAsync();
        var big = Rational.Create(BigInteger.Pow(10, 60) + 1, BigInteger.Pow(10, 60));
        var cmd = new NpgsqlCommand("SELECT serk_rational_cmp(@a, @b), serk_rational_cmp(@b, @a), serk_rational_cmp(@a, @a), serk_rational_cmp('-1/3', '-333333333333/1000000000000')", conn);
        cmd.Parameters.Add(RationalSql.Parameter("a", big));
        cmd.Parameters.Add(RationalSql.Parameter("b", Rational.One));
        await using var r = await cmd.ExecuteReaderAsync();
        Assert.True(await r.ReadAsync());
        Assert.Equal(1, r.GetInt32(0));
        Assert.Equal(-1, r.GetInt32(1));
        Assert.Equal(0, r.GetInt32(2));
        Assert.Equal(-1, r.GetInt32(3));
    }
}
