package pages

import "github.com/gsxhq/newfixture/pages/parts"

func Index() gsx.Node {
	return (
		<div>
			hi from newfixture <parts.Footer/>
		</div>
	)
}
