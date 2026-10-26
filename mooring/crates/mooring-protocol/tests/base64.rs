use mooring_protocol::boundary::{decode_base64, encode_base64, BoundaryError};

#[test]
fn canonical_vectors_and_caller_caps_cover_each_padding_case() {
    for (plain, encoded) in [
        ("", ""),
        ("f", "Zg=="),
        ("fo", "Zm8="),
        ("foo", "Zm9v"),
        ("foob", "Zm9vYg=="),
        ("fooba", "Zm9vYmE="),
        ("foobar", "Zm9vYmFy"),
    ] {
        assert_eq!(
            encode_base64(plain.as_bytes(), plain.len()).unwrap(),
            encoded
        );
        assert_eq!(
            decode_base64(encoded, plain.len()).unwrap(),
            plain.as_bytes()
        );
        if !plain.is_empty() {
            assert!(matches!(
                encode_base64(plain.as_bytes(), plain.len() - 1),
                Err(BoundaryError::Capacity)
            ));
            assert!(matches!(
                decode_base64(encoded, plain.len() - 1),
                Err(BoundaryError::Capacity)
            ));
        }
    }
    let all_bytes: Vec<u8> = (0..=255).collect();
    let encoded = encode_base64(&all_bytes, 256).unwrap();
    assert_eq!(decode_base64(&encoded, 256).unwrap(), all_bytes);
    assert_eq!(encode_base64(&[255, 255, 255], 3).unwrap(), "////");
}

#[test]
fn noncanonical_and_malformed_inputs_never_produce_decoded_bytes() {
    for encoded in [
        "Zg", "Zg=", "Zg===", "Zh==", "Zm9=", "Z=g=", "=g==", "====", "Zg==\n", "Zg==Zg==", "----",
        "____", "\0AAA", "éAA",
    ] {
        assert!(decode_base64(encoded, usize::MAX).is_err(), "{encoded:?}");
    }
    // A syntactically valid encoded value is rejected by the size preflight
    // before the decoder reserves any output storage.
    assert!(matches!(
        decode_base64("AAAA", 2),
        Err(BoundaryError::Capacity)
    ));
}
