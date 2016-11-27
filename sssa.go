package sssa

import (
	"encoding/base64"
	"math/big"
)

var Prime *big.Int

func init() {
	// Set constant prime across the package
	Prime, _ = big.NewInt(0).SetString("115792089237316195423570985008687907853269984665640564039457584007913129639747", 10)
}

/**
 * Returns a new array of secret shares (encoding x,y pairs as base64 strings)
 * created by Shamir's Secret Sharing Algorithm requring a minimum number of
 * share to recreate, of length shares, from the input secret raw as a string
**/
func Create(minimum int, shares int, raw string) []string {
	b := CreateBytes(minimum, shares, []byte(raw))
	if b == nil {
		return nil
	}
	strings := make([]string, shares)
	for i, bytes := range b {
		strings[i] = base64.RawURLEncoding.EncodeToString(bytes)
	}
	return strings
}

func CreateBytes(minimum int, shares int, raw []byte) [][]byte {
	// Verify minimum isn't greater than shares; there is no way to recreate
	// the original polynomial in our current setup, therefore it doesn't make
	// sense to generate fewer shares than are needed to reconstruct the secret.
	// [TODO]: proper error handling
	if minimum > shares {
		return nil
	}

	// Convert the secret to its respective 256-bit big.Int representation
	var secret []*big.Int = splitByteToInt(raw)

	// List of currently used numbers in the polynomial
	var numbers []*big.Int = make([]*big.Int, 0)
	numbers = append(numbers, big.NewInt(0))

	// Create the polynomial of degree (minimum - 1); that is, the
	// highest order term is (minimum-1), though as there is a
	// constant term with order 0, there are (minimum) number of
	// coefficients.
	//
	// However, the polynomial object is a 2d array, because we
	// are constructing a different polynomial for each part of
	// the secret polynomial[parts][minimum]
	var polynomial [][]*big.Int = make([][]*big.Int, len(secret))
	for i := range polynomial {
		polynomial[i] = make([]*big.Int, minimum)
		polynomial[i][0] = secret[i]

		for j := range polynomial[i][1:] {
			// Each coefficient should be unique
			number := random()
			for inNumbers(numbers, number) {
				number = random()
			}
			numbers = append(numbers, number)

			polynomial[i][j+1] = number
		}
	}

	// Create the secrets object; this holds the (x, y) points of
	// each share.  Again, because secret is an array, each share
	// could have multiple parts over which we are computing
	// Shamir's Algorithm. The last dimension is always two, as it
	// is storing an x, y pair of points.
	//
	// Note: this array is technically unnecessary due to creating
	// result in the inner loop. Can disappear later if
	// desired. [TODO]
	//
	// secrets[shares][parts][2]
	var secrets [][][]*big.Int = make([][][]*big.Int, shares)
	var result []string = make([]string, shares)
	var resultBytes [][]byte = make([][]byte, shares)

	// For every share...
	for i := range secrets {
		secrets[i] = make([][]*big.Int, len(secret))
		// ...and every part of the secret...
		for j := range secrets[i] {
			secrets[i][j] = make([]*big.Int, 2)

			// ...generate a new x-coordinate...
			number := random()
			for inNumbers(numbers, number) {
				number = random()
			}
			numbers = append(numbers, number)

			// ...and evaluate the polynomial at that point...
			secrets[i][j][0] = number
			secrets[i][j][1] = evaluatePolynomial(polynomial[j], number)

			// ...add it to results...
			log.Debug("secrets[%d][%d][0] = %x (%dB)", i, j,
				secrets[i][j][0],
				len(secrets[i][j][0].Bytes()))
			log.Debug("secrets[%d][%d][1] = %x (%dB)", i, j,
				secrets[i][j][1],
				len(secrets[i][j][1].Bytes()))
			// each of secrets[i][j][*] is < 256^32
			result[i] += toBase64(secrets[i][j][0])
			result[i] += toBase64(secrets[i][j][1])
			resultBytes[i] = appendBytes(resultBytes[i], secrets[i][j][0])
			resultBytes[i] = appendBytes(resultBytes[i], secrets[i][j][1])
			log.Info("resultBytes[%d] is now %d", i, len(resultBytes[i]))

		}
	}

	// ...and return!
	return resultBytes
}

/**
 * Takes a string array of shares encoded in base64 created via Shamir's
 * Algorithm; each string represent a byte array of an equal length of a
 * multiple of 64 bytes as a single 64 byte share is a pair of 256-bit
 * numbers (x, y).
 *
 * Note: the polynomial will converge if the specified minimum number of shares
 *       or more are passed to this function. Passing more does not affect it.
 *       Passing fewer however, simply means that the returned secret is wrong.
**/

func Combine(shares []string) string {
	b := make([][]byte, len(shares))
	for i, str := range shares {
		bytes, err := base64.RawURLEncoding.DecodeString(str)
		if err != nil {
			// invalid encoding
			return ""
		}
		b[i] = bytes
	}

	return string(CombineBytes(b))
}

func CombineBytes(shares [][]byte) []byte {
	// Recreate the original object of x, y points, based upon number of shares
	// and size of each share (number of parts in the secret).
	var secrets [][][]*big.Int = make([][][]*big.Int, len(shares))

	// For each share...
	for i := range shares {
		// ...ensure that it is valid...
		if IsValidShare(shares[i]) == false {
			return nil
		}

		// ...find the number of parts it represents...
		share := shares[i]
		count := len(share) / 64
		secrets[i] = make([][]*big.Int, count)

		// ...and for each part, find the x,y pair...
		for j := range secrets[i] {
			cshare := share[j*64 : (j+1)*64]
			secrets[i][j] = make([]*big.Int, 2)
			// ...decoding from base 64.
			secrets[i][j][0] = from32Bytes(cshare[:32])
			secrets[i][j][1] = from32Bytes(cshare[32:])
		}
	}

	// Use Lagrange Polynomial Interpolation (LPI) to reconstruct the secret.
	// For each part of the secert (clearest to iterate over)...
	var secret []*big.Int = make([]*big.Int, len(secrets[0]))
	for j := range secret {
		secret[j] = big.NewInt(0)
		// ...and every share...
		for i := range secrets { // LPI sum loop
			// ...remember the current x and y values...
			origin := secrets[i][j][0]
			originy := secrets[i][j][1]
			numerator := big.NewInt(1)   // LPI numerator
			denominator := big.NewInt(1) // LPI denominator
			// ...and for every other point...
			for k := range secrets { // LPI product loop
				if k != i {
					// ...combine them via half products...
					current := secrets[k][j][0]
					negative := big.NewInt(0)
					negative = negative.Mul(current, big.NewInt(-1))
					added := big.NewInt(0)
					added = added.Sub(origin, current)

					numerator = numerator.Mul(numerator, negative)
					numerator = numerator.Mod(numerator, Prime)

					denominator = denominator.Mul(denominator, added)
					denominator = denominator.Mod(denominator, Prime)
				}
			}

			// LPI product
			// ...multiply together the points (y)(numerator)(denominator)^-1...
			working := big.NewInt(0).Set(originy)
			working = working.Mul(working, numerator)
			working = working.Mul(working, modInverse(denominator))

			// LPI sum
			secret[j] = secret[j].Add(secret[j], working)
			secret[j] = secret[j].Mod(secret[j], Prime)
		}
	}

	// ...and return the result!
	return mergeIntToByte(secret)
}

/**
 * Takes in a given string to check if it is a valid secret
 *
 * Requirements:
 * 	- Length multiple of 64
 *	- No 32-byte number is less than 0 or greater than the Prime
 *
 * Returns only success/failure (bool)
**/
func IsValidShare(candidate []byte) bool {
	if len(candidate)%64 != 0 {
		return false
	}

	count := len(candidate) / 32
	for j := 0; j < count; j++ {
		part := candidate[j*32 : (j+1)*32]
		decode := big.NewInt(0).SetBytes(part)
		if decode.Cmp(big.NewInt(0)) == -1 || decode.Cmp(Prime) == 1 {
			return false
		}
	}

	return true
}
