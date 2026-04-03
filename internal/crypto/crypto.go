package crypto

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "crypto/rsa"
    "crypto/x509"
    "encoding/base64"
    "encoding/json"
    "encoding/pem"
    "fmt"
    "io"
    "os"
    "path/filepath"
)

type Envelope struct {
    EncryptedKey string `json:"encrypted_key"`
    Nonce        string `json:"nonce"`
    Ciphertext   string `json:"ciphertext"`
}

func EncryptFile(inputPath, outputPath, pubKeyPath string) error {
    plaintext, err := os.ReadFile(inputPath)
    if err != nil {
        return err
    }
    payload, err := EncryptBytes(plaintext, pubKeyPath)
    if err != nil {
        return err
    }
    if outputPath == "" {
        outputPath = inputPath + ".enc"
    }
    return os.WriteFile(outputPath, payload, 0o600)
}

func EncryptBytes(plaintext []byte, pubKeyPath string) ([]byte, error) {
    pubKey, err := loadPublicKey(pubKeyPath)
    if err != nil {
        return nil, err
    }
    aesKey := make([]byte, 32)
    if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
        return nil, err
    }
    block, err := aes.NewCipher(aesKey)
    if err != nil {
        return nil, err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return nil, err
    }
    ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
    encryptedKey, err := rsa.EncryptPKCS1v15(rand.Reader, pubKey, aesKey)
    if err != nil {
        return nil, err
    }
    env := Envelope{
        EncryptedKey: base64.StdEncoding.EncodeToString(encryptedKey),
        Nonce:        base64.StdEncoding.EncodeToString(nonce),
        Ciphertext:   base64.StdEncoding.EncodeToString(ciphertext),
    }
    return json.MarshalIndent(env, "", "  ")
}

func DecryptToBytes(inputPath, privateKeyPath string) ([]byte, error) {
    b, err := os.ReadFile(inputPath)
    if err != nil {
        return nil, err
    }
    return decryptBytes(b, privateKeyPath)
}

func DecryptToString(inputPath, privateKeyPath string) (string, error) {
    b, err := DecryptToBytes(inputPath, privateKeyPath)
    if err != nil {
        return "", err
    }
    return string(b), nil
}

func DecryptFile(inputPath, outputPath, privateKeyPath string) error {
    plaintext, err := DecryptToBytes(inputPath, privateKeyPath)
    if err != nil {
        return err
    }
    if outputPath == "" {
        fmt.Print(string(plaintext))
        return nil
    }
    return os.WriteFile(outputPath, plaintext, 0o600)
}

func decryptBytes(b []byte, privateKeyPath string) ([]byte, error) {
    var env Envelope
    if err := json.Unmarshal(b, &env); err != nil {
        return nil, err
    }
    privKey, err := loadPrivateKey(privateKeyPath)
    if err != nil {
        return nil, err
    }
    encryptedKey, err := base64.StdEncoding.DecodeString(env.EncryptedKey)
    if err != nil {
        return nil, err
    }
    aesKey, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, encryptedKey)
    if err != nil {
        return nil, err
    }
    nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
    if err != nil {
        return nil, err
    }
    ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
    if err != nil {
        return nil, err
    }
    block, err := aes.NewCipher(aesKey)
    if err != nil {
        return nil, err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    return gcm.Open(nil, nonce, ciphertext, nil)
}

func loadPublicKey(path string) (*rsa.PublicKey, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    block, _ := pem.Decode(b)
    if block == nil {
        return nil, fmt.Errorf("invalid public key PEM")
    }
    pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
    if err != nil {
        return nil, err
    }
    pub, ok := pubAny.(*rsa.PublicKey)
    if !ok {
        return nil, fmt.Errorf("public key is not RSA")
    }
    return pub, nil
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    block, _ := pem.Decode(b)
    if block == nil {
        return nil, fmt.Errorf("invalid private key PEM")
    }
    if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
        return key, nil
    }
    anyKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
    if err != nil {
        return nil, err
    }
    key, ok := anyKey.(*rsa.PrivateKey)
    if !ok {
        return nil, fmt.Errorf("private key is not RSA")
    }
    return key, nil
}

func DefaultPrivateKeyPath() string {
    if p := os.Getenv("PORTA_PRIVATE_KEY"); p != "" {
        return p
    }
    home, _ := os.UserHomeDir()
    return filepath.Join(home, ".porta", "private.pem")
}
