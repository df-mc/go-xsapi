package mpsd_test

/*
func ExampleClient_Publish() {
	client := test.Client()
	defer client.Close()

	properties, _ := json.Marshal(map[string]any{})
	session, err := client.MPSD().Publish(context.TODO(), mpsd.SessionReference{
		ServiceConfigID: uuid.MustParse("4fc10100-5f7a-4470-899b-280835760c07"),
		TemplateName:    "MinecraftLobby",
	}, mpsd.PublishConfig{
		CustomProperties: properties,
		JoinRestriction:  mpsd.SessionRestrictionFollowed,
		ReadRestriction:  mpsd.SessionRestrictionFollowed,
	})
	if err != nil {
		panic(err)
	}
	defer session.Close()
}
*/
