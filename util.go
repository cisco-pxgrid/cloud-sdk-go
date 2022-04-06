package cloud

func zeroByteArray(arr []byte) {
	for i := 0; i < len(arr); i++ {
		arr[i] = 0
	}
}
